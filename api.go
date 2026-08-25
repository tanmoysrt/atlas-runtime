package main

import (
	_ "embed"

	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed dashboard.html
var dashboardHTML string

// API exposes the runtime's HTTP and WebSocket endpoints.
type API struct {
	runtime         *Runtime
	tokens          map[string]time.Time
	mutex           sync.Mutex
	upgrader        websocket.Upgrader
	enableDashboard bool
}

// NewAPI creates a new API handler for the given runtime.
func NewAPI(runtime *Runtime, enableDashboard bool) *API {
	return &API{
		runtime:         runtime,
		tokens:          make(map[string]time.Time),
		upgrader:        websocket.Upgrader{CheckOrigin: func(request *http.Request) bool { return true }},
		enableDashboard: enableDashboard,
	}
}

// Serve starts the HTTP server and blocks until the context is cancelled.
func (api *API) Serve(ctx context.Context, address string) error {
	serveMux := http.NewServeMux()

	if api.enableDashboard {
		serveMux.HandleFunc("GET /{$}", api.handleDashboard)
	}

	// Lifecycle endpoints.
	serveMux.HandleFunc("GET /info", api.handleInfo)
	serveMux.HandleFunc("GET /health", api.handleHealth)
	serveMux.HandleFunc("POST /start", api.wrap(api.runtime.Start))
	serveMux.HandleFunc("POST /stop", api.wrap(api.runtime.Stop))
	serveMux.HandleFunc("POST /reboot", api.wrap(api.runtime.Reboot))
	serveMux.HandleFunc("POST /reload", api.wrap(api.runtime.Reload))
	serveMux.HandleFunc("POST /resize", api.handleResize)
	serveMux.HandleFunc("POST /rootfs", api.handleRootfs)
	serveMux.HandleFunc("POST /sysrq", api.handleSysRq)

	// Console endpoints.
	serveMux.HandleFunc("POST /console", api.handleConsoleToken)
	serveMux.HandleFunc("GET /console/attach", api.handleConsoleWebSocket)

	// SSH key management.
	serveMux.HandleFunc("GET /ssh-keys", api.handleSSHKeysGet)
	serveMux.HandleFunc("POST /ssh-keys", api.handleSSHKeysPost)
	serveMux.HandleFunc("DELETE /ssh-keys/{id}", api.handleSSHKeysDelete)

	// Network and snapshot endpoints.
	serveMux.HandleFunc("PUT /network/bandwidth", api.handleBandwidth)
	serveMux.HandleFunc("PUT /network/public-ip", api.handlePublicIP)
	serveMux.HandleFunc("POST /snapshot", api.handleSnapshot)

	server := &http.Server{Addr: address, Handler: serveMux}
	go func() { <-ctx.Done(); server.Shutdown(context.Background()) }()
	return server.ListenAndServe()
}

func (api *API) handleDashboard(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Write([]byte(dashboardHTML))
}

// decode reads JSON from the request body into the target value.
// It writes a 400 Bad Request and returns false on error.
func (api *API) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// wrap turns a plain error-returning function into an HTTP handler.
// Any error is returned as a 500 Internal Server Error.
func (api *API) wrap(handlerFunction func() error) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if err := handlerFunction(); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (api *API) handleInfo(writer http.ResponseWriter, request *http.Request) {
	json.NewEncoder(writer).Encode(map[string]any{
		"instance_id":   api.runtime.meta.InstanceID,
		"hostname":      api.runtime.meta.Hostname,
		"initialized":   api.runtime.meta.Initialized,
		"desired_state": api.runtime.meta.DesiredState,
		"config":        api.runtime.config,
	})
}

func (api *API) handleHealth(writer http.ResponseWriter, request *http.Request) {
	writer.WriteHeader(http.StatusOK)
}

func (api *API) handleResize(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CPUs   int   `json:"cpus"`
		Memory int64 `json:"memory"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.runtime.Resize(body.CPUs, body.Memory); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (api *API) handleRootfs(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Size      int64 `json:"size"`
		Bandwidth int64 `json:"bandwidth"`
		IOPS      int   `json:"iops"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.runtime.ResizeRootfs(body.Size, body.Bandwidth, body.IOPS); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (api *API) handleSysRq(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.runtime.SysRq(body.Key); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (api *API) handleConsoleToken(writer http.ResponseWriter, request *http.Request) {
	randomBytes := make([]byte, 16)
	rand.Read(randomBytes)
	token := hex.EncodeToString(randomBytes)

	api.mutex.Lock()
	api.tokens[token] = time.Now().Add(60 * time.Second)
	api.mutex.Unlock()

	json.NewEncoder(writer).Encode(map[string]string{"token": token})
}

func (api *API) handleConsoleWebSocket(writer http.ResponseWriter, request *http.Request) {
	token := request.URL.Query().Get("token")

	api.mutex.Lock()
	expiry, found := api.tokens[token]
	delete(api.tokens, token)
	api.mutex.Unlock()

	if !found || time.Now().After(expiry) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}

	connection, err := api.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()

	api.runtime.console.AddConn(connection)
	defer api.runtime.console.RemoveConn(connection)

	// Send the current ring buffer so new clients see history.
	connection.WriteMessage(websocket.BinaryMessage, api.runtime.console.ReadRing())

	// Forward WebSocket input to the serial FIFO.
	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		api.runtime.console.WriteInput(payload)
	}
}

func (api *API) handleSSHKeysGet(writer http.ResponseWriter, request *http.Request) {
	json.NewEncoder(writer).Encode(api.runtime.config.SSH.AuthorizedKeys)
}

func (api *API) handleSSHKeysPost(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.mutateConfig(func(config *Config) {
		config.SSH.AuthorizedKeys = append(config.SSH.AuthorizedKeys, body.Key)
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (api *API) handleSSHKeysDelete(writer http.ResponseWriter, request *http.Request) {
	id, _ := strconv.Atoi(request.PathValue("id"))
	if err := api.mutateConfig(func(config *Config) {
		if id >= 0 && id < len(config.SSH.AuthorizedKeys) {
			config.SSH.AuthorizedKeys = append(config.SSH.AuthorizedKeys[:id], config.SSH.AuthorizedKeys[id+1:]...)
		}
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (api *API) handleBandwidth(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Ingress int64 `json:"ingress_bandwidth"`
		Egress  int64 `json:"egress_bandwidth"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.mutateConfig(func(config *Config) {
		config.Network.IngressBandwidth = body.Ingress
		config.Network.EgressBandwidth = body.Egress
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

// handlePublicIP sets or clears the VM's 1:1 NAT public IPv4/IPv6 addresses.
// An empty string clears that address. Applied live via Reload, no VM restart needed.
func (api *API) handlePublicIP(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		IPv4 string `json:"public_ipv4"`
		IPv6 string `json:"public_ipv6"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.mutateConfig(func(config *Config) {
		config.Network.PublicIPv4 = body.IPv4
		config.Network.PublicIPv6 = body.IPv6
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (api *API) handleSnapshot(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !api.decode(writer, request, &body) {
		return
	}
	if err := api.runtime.Snapshot(body.ID); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

// mutateConfig loads config.toml, applies the mutation, writes it back
// atomically, and applies the same in-memory value to the running instance.
func (api *API) mutateConfig(mutation func(*Config)) error {
	config, err := LoadConfig(api.runtime.configPath)
	if err != nil {
		return err
	}

	mutation(config)
	if err := config.Validate(); err != nil {
		return err
	}
	if err := SaveConfig(api.runtime.configPath, config); err != nil {
		return err
	}
	return api.runtime.applyConfig(config)
}
