package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

type Metrics struct {
	OS          string        `json:"os"`
	Hostname    string        `json:"hostname"`
	Uptime      uint64        `json:"uptimeSeconds"`
	CpuTemp     float64       `json:"cpuTemp"`
	CpuUsage    float64       `json:"cpuUsage"`
	Memory      MemoryUsage   `json:"memoryUsage"`
	Storage     DiskUsage     `json:"diskUsage"`
	LastUpdated string        `json:"lastUpdated"`
}

type MemoryUsage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

type DiskUsage struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

var upgrader = websocket.Upgrader{
	// restrict origin to mayurbhoi.com ip
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var clients = make(map[*websocket.Conn]bool)
var clientsMutex = sync.Mutex{}

func getCpuTemp() (float64, error) {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0, err
	}
	temp, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, err
	}
	return temp / 1000.0, nil
}

func collectMetrics() (Metrics, error) {
	cpuPercentages, err := cpu.Percent(time.Second, false)
	if err != nil || len(cpuPercentages) == 0 {
		return Metrics{}, err
	}

	vMem, err := mem.VirtualMemory()
	if err != nil {
		return Metrics{}, err
	}

	dUsage, err := disk.Usage("/")
	if err != nil {
		return Metrics{}, err
	}

	cpuTemp, err := getCpuTemp()
	if err != nil {
		// don't fail, just set temp to 0.
		log.Printf("Could not read CPU temp: %v. Setting to 0.", err)
		cpuTemp = 0
	}

	hostInfo, err := host.Info()
	if err != nil {
		return Metrics{}, err
	}

	metrics := Metrics{
		OS:          runtime.GOOS,
		Hostname:    hostInfo.Hostname,
		Uptime:      hostInfo.Uptime,
		CpuTemp:     cpuTemp,
		CpuUsage:    cpuPercentages[0],
		Memory:      MemoryUsage{Total: vMem.Total, Used: vMem.Used, Free: vMem.Free},
		Storage:     DiskUsage{Total: dUsage.Total, Used: dUsage.Used, Free: dUsage.Free},
		LastUpdated: time.Now().Format(time.RFC3339),
	}
	return metrics, nil
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	clientsMutex.Lock()
	clients[ws] = true
	clientsMutex.Unlock()

	log.Println("Client connected")

	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			log.Printf("Client disconnected: %v", err)
			break
		}
	}

	clientsMutex.Lock()
	delete(clients, ws)
	clientsMutex.Unlock()
}

func broadcastMetrics() {
	// send data every 500ms.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		metrics, err := collectMetrics()
		if err != nil {
			log.Printf("Error collecting metrics: %v", err)
			continue
		}

		message, err := json.Marshal(metrics)
		if err != nil {
			log.Printf("Error marshaling metrics: %v", err)
			continue
		}

		clientsMutex.Lock()
		for client := range clients {
			err := client.WriteMessage(websocket.TextMessage, message)
			if err != nil {
				log.Printf("Error writing to client: %v", err)
				client.Close()
				delete(clients, client)
			}
		}
		clientsMutex.Unlock()
	}
}

func main() {
	http.HandleFunc("/ws", handleConnections)

	// start broadcasting metrics in a separate goroutine
	go broadcastMetrics()

	port := "8080"
	log.Printf("Starting WebSocket server on port %s...", port)
	log.Printf("Endpoint available at ws://<pi-ip>:%s/ws", port)

	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
