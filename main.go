package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var (
	rdb *redis.Client
	hub *Hub
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Hub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan string
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.Mutex
}

func newHub() *Hub {
	return &Hub{
		clients:    make(map[*websocket.Conn]bool),
		broadcast:  make(chan string),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
	}
}

func (h *Hub) run() {
	for {
		select {
		case conn := <-h.register:
			h.mutex.Lock()
			h.clients[conn] = true
			h.mutex.Unlock()
		case conn := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[conn]; ok {
				delete(h.clients, conn)
				conn.Close()
			}
			h.mutex.Unlock()
		case message := <-h.broadcast:
			h.mutex.Lock()
			for conn := range h.clients {
				err := conn.WriteMessage(websocket.TextMessage, []byte(message))
				if err != nil {
					conn.Close()
					delete(h.clients, conn)
				}
			}
			h.mutex.Unlock()
		}
	}
}

func main() {
	// Initialize Redis client
	rdb = redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	hub = newHub()
	go hub.run()

	r := mux.NewRouter()

	r.HandleFunc("/update-score", updateScoreHandler).Methods("POST")
	r.HandleFunc("/leaderboard", getLeaderboardHandler).Methods("GET")
	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Error while upgrading connection:", err)
			return
		}
		hub.register <- conn
		defer func() {
			hub.unregister <- conn
		}()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}).Methods("GET")

	corsHandler := handlers.CORS(
		handlers.AllowedOrigins([]string{"http://localhost:5173"}),
		handlers.AllowedHeaders([]string{"Content-Type", "Authorization"}),
		handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"}),
	)(r)

	log.Fatal(http.ListenAndServe(":8080", corsHandler))
}

func updateScoreHandler(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	var data struct {
		Username string `json:"username"`
		Score    int    `json:"score"`
	}

	err := json.NewDecoder(r.Body).Decode(&data)
	if err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	_, err = rdb.ZIncrBy(ctx, "leaderboard", float64(data.Score), data.Username).Result()
	if err != nil {
		http.Error(w, "Failed to update score", http.StatusInternalServerError)
		return
	}

	leaderboard, err := rdb.ZRevRangeWithScores(ctx, "leaderboard", 0, -1).Result()
	if err != nil {
		http.Error(w, "Failed to fetch leaderboard", http.StatusInternalServerError)
		return
	}

	leaderboardJSON, err := json.Marshal(leaderboard)
	if err != nil {
		http.Error(w, "Failed to serialize leaderboard", http.StatusInternalServerError)
		return
	}

	hub.broadcast <- string(leaderboardJSON)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Score updated successfully"))
}

func getLeaderboardHandler(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	leaderboard, err := rdb.ZRevRangeWithScores(ctx, "leaderboard", 0, -1).Result()
	if err != nil {
		http.Error(w, "Failed to fetch leaderboard", http.StatusInternalServerError)
		return
	}

	err = json.NewEncoder(w).Encode(leaderboard)
	if err != nil {
		http.Error(w, "Failed to encode leaderboard", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
