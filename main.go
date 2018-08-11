package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type player struct {
	id   string
	x    int
	y    int
	dead bool
	conn *websocket.Conn
}

type message struct {
	Type string `json:"t"`
	ID   string `json:"i"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

func (p *player) sendMySpawn() {
	err := p.conn.WriteJSON(message{
		Type: "ms",
		ID:   p.id,
		X:    p.x,
		Y:    p.y,
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

func (p *player) sendSpawn(player *player) {
	err := p.conn.WriteJSON(message{
		Type: "s",
		ID:   player.id,
		X:    player.x,
		Y:    player.y,
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

func (p *player) sendMove(player *player) {
	err := p.conn.WriteJSON(message{
		Type: "m",
		ID:   player.id,
		X:    player.x,
		Y:    player.y,
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

func (p *player) sendMyDeath() {
	err := p.conn.WriteJSON(message{
		Type: "md",
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

func (p *player) sendDeath(player *player) {
	err := p.conn.WriteJSON(message{
		Type: "d",
		ID:   player.id,
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

func (p *player) sendBlown(x, y int) {
	err := p.conn.WriteJSON(message{
		Type: "b",
		X:    x,
		Y:    y,
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

func (p *player) restoreGrid() {
	err := p.conn.WriteJSON(message{
		Type: "rg",
	})
	if err != nil {
		fmt.Println("failed...", err)
	}
}

type platform struct {
	blown bool
}

type game struct {
	isOngoing    bool
	blown        int
	playersAlive int
}

var players = make(map[string]*player)
var grid = make(map[int]*platform, 4*4)
var gameMutex = &sync.RWMutex{}
var gameState = game{
	isOngoing:    false,
	blown:        0,
	playersAlive: 0,
}

func sendDisconnectAll(player *player) {
	gameMutex.RLock()
	for _, p := range players {
		if p != player {
			p.sendDeath(player)
		}
	}
	gameMutex.RUnlock()
	gameMutex.Lock()
	if !player.dead {
		gameState.playersAlive--
	}
	delete(players, player.id)
	gameMutex.Unlock()
}

func blowRandom() {
	for {
		buf := make([]byte, 1)
		n, err := rand.Read(buf)
		for n != 1 || err != nil {
			n, err = rand.Read(buf)
		}

		pindex := int(buf[0] % 16)
		gameMutex.Lock()
		if grid[pindex].blown {
			gameMutex.Unlock()
			continue
		}

		x := (pindex % 4) * 128
		x2 := ((pindex % 4) + 1) * 128
		y := (pindex / 4) * 128
		y2 := ((pindex / 4) + 1) * 128

		grid[pindex].blown = true
		deadPlayers := make([]*player, 0)
		for _, p := range players {
			p.sendBlown((pindex % 4), (pindex / 4))
			if !p.dead && p.x >= x && p.x <= x2 && p.y >= y && p.y <= y2 {
				p.dead = true
				p.sendMyDeath()
				deadPlayers = append(deadPlayers, p)
				gameState.playersAlive--
			}
		}
		for _, p := range players {
			for _, dead := range deadPlayers {
				if p != dead {
					p.sendDeath(dead)
				}
			}
		}
		gameState.blown++
		gameMutex.Unlock()
		return
	}

}

// Config struct
type Config struct {
	Address     string `json:"address"`
	Host        string `json:"host"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private-key"`
}

const configFile = "conf.json"

var conf Config

func createConfig() error {
	conf.Address = ":4445"
	conf.Host = ""
	conf.Certificate = "cert.pem"
	conf.PrivateKey = "privkey.pem"
	return nil
}

func initPlayer(conn *websocket.Conn) {
	buf := make([]byte, 10)
	n, err := rand.Read(buf)
	for n != 10 || err != nil {
		n, err = rand.Read(buf)
	}

	var player = &player{
		id:   base64.StdEncoding.EncodeToString(buf),
		x:    0,
		y:    0,
		conn: conn,
		dead: true,
	}

	gameMutex.Lock()
	players[player.id] = player
	gameMutex.Unlock()

	fmt.Printf("%s connected.\n", conn.RemoteAddr())

	gameMutex.RLock()
	for _, p := range players {
		if p != player && !p.dead {
			player.sendSpawn(p)
			if !gameState.isOngoing {
				p.sendSpawn(player)
			}
		}
	}
	gameMutex.RUnlock()

	for i, p := range grid {
		if p.blown {
			x := i % 4
			y := i / 4
			player.sendBlown(x, y)
		}
	}

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("%s disconnected. Reason: %s\n", conn.RemoteAddr(), err)
			sendDisconnectAll(player)
			return
		}

		if msgType == websocket.TextMessage {
			var inboundMsg *message
			err := json.Unmarshal(msg, &inboundMsg)
			if err != nil {
				fmt.Printf("%s disconnected. Reason: %s\n", conn.RemoteAddr(), err)
				sendDisconnectAll(player)
				return
			}
			gameMutex.RLock()
			if player.dead {
				gameMutex.RUnlock()
				continue
			} else {
				gameMutex.RUnlock()
			}

			if inboundMsg.X >= 0 && inboundMsg.X <= 512 && inboundMsg.Y >= 0 && inboundMsg.Y <= 512 {
				gameMutex.Lock()
				px := int(math.Min(float64(inboundMsg.X/128), 3))
				py := int(math.Min(float64(inboundMsg.Y/128), 3))
				if !grid[px+py*4].blown {
					player.x = inboundMsg.X
					player.y = inboundMsg.Y
				} else {
					player.sendMySpawn()
				}
				gameMutex.Unlock()

				gameMutex.RLock()
				for _, p := range players {
					if p != player {
						p.sendMove(player)
					}
				}
				gameMutex.RUnlock()
			}
		}
	}
}

func main() {
	for i := 0; i < 16; i++ {
		grid[i] = &platform{
			blown: false,
		}
	}

	go func() {
		for {
			gameMutex.RLock()
			if !gameState.isOngoing {
				if len(players) > 0 {
					gameMutex.RUnlock()

					gameMutex.Lock()
					for i := 0; i < 16; i++ {
						grid[i].blown = false
					}
					gameState.blown = 0

					for _, p := range players {
						p.dead = false
						p.sendMySpawn()
						p.restoreGrid()

						for _, p2 := range players {
							if p != p2 {
								p.sendSpawn(p2)
							}
						}
					}
					gameState.playersAlive = len(players)
					gameState.isOngoing = true
					gameMutex.Unlock()
				} else {
					gameMutex.RUnlock()
					time.Sleep(4000 * time.Millisecond)
					continue
				}
				time.Sleep(1000 * time.Millisecond)
			} else {
				gameMutex.RUnlock()
			}

			gameMutex.RLock()
			if gameState.blown <= 15 && gameState.playersAlive > 0 {
				gameMutex.RUnlock()
				blowRandom()
			} else {
				gameMutex.RUnlock()
			}

			gameMutex.RLock()
			if gameState.playersAlive == 0 {
				gameMutex.RUnlock()
				gameState.isOngoing = false
			} else {
				gameMutex.RUnlock()
			}

			time.Sleep(1000 * time.Millisecond)
		}
	}()

	file, err := os.Open(configFile)
	if err != nil {
		fmt.Println("Could not open config file:", err)
		if err := createConfig(); err != nil {
			fmt.Println("Failed to create new config file:", err)
			return
		}
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&conf); err != nil {
		fmt.Println("Could not parse config file:", err)
		return
	}
	file.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/game", func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil) // error ignored for sake of simplicity
		go initPlayer(conn)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	mux.HandleFunc("/outofspace.zip", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "outofspace.zip")
	})

	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{
			tls.CurveP256,
			tls.X25519,
		},
		PreferServerCipherSuites: true,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}

	srv := &http.Server{
		Addr:         conf.Address,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
		Handler:      mux,
		TLSConfig:    cfg,
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler), 0),
	}

	log.Println("Listening...")
	log.Fatal(srv.ListenAndServeTLS(conf.Certificate, conf.PrivateKey))
}
