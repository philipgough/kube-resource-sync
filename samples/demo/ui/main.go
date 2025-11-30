package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	IsDir   bool   `json:"isDir"`
}

type Hub struct {
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan FileContent
	mu         sync.RWMutex
	lastHash   string // Track file hash to avoid unnecessary updates
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan FileContent
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var watchDir = getWatchDir()

func getWatchDir() string {
	if dir := os.Getenv("WATCH_DIR"); dir != "" {
		return dir
	}
	if _, err := os.Stat("/config"); err == nil {
		return "/config"
	}
	return "./watched"
}

func getWatchFile() string {
	return os.Getenv("WATCH_FILE")
}

// refresh reloads the watched file - inspired by Thanos ConfigWatcher.refresh
func (h *Hub) refresh() {
	watchFile := getWatchFile()
	if watchFile == "" {
		return
	}

	filePath := filepath.Join(watchDir, watchFile)
	data, err := os.ReadFile(filePath)
	if err != nil {
		log.Printf("Error reading file %s: %v", filePath, err)
		return
	}

	// Hash-based change detection like Thanos
	content := string(data)
	hash := fmt.Sprintf("%x", md5.Sum(data))
	
	h.mu.Lock()
	if h.lastHash == hash {
		h.mu.Unlock()
		log.Printf("File %s hash unchanged, skipping update", watchFile)
		return
	}
	h.lastHash = hash
	h.mu.Unlock()

	log.Printf("File %s changed (hash: %s, size: %d bytes)", watchFile, hash[:8], len(content))
	
	fileContent := FileContent{
		Path:    watchFile,
		Content: content,
		IsDir:   false,
	}
	
	h.broadcast <- fileContent
}


func newHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan FileContent),
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Println("Client connected")

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			log.Println("Client disconnected")

		case content := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- content:
				default:
					delete(h.clients, client)
					close(client.send)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for {
		select {
		case content, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			jsonBytes, _ := json.Marshal(content)
			log.Printf("Sending JSON to client: %s", string(jsonBytes))
			c.conn.WriteJSON(content)
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func wsHandler(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}

	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan FileContent, 256),
	}

	client.hub.register <- client

	go client.writePump()
	go client.readPump()

	sendInitialContent(client)
}

func sendInitialContent(client *Client) {
	log.Println("Sending initial content to client")
	watchFile := getWatchFile()
	
	if watchFile != "" {
		// Watch a specific file
		filePath := filepath.Join(watchDir, watchFile)
		if data, err := os.ReadFile(filePath); err == nil {
			content := string(data)
			// Initialize hash on first load like Thanos
			hash := fmt.Sprintf("%x", md5.Sum(data))
			client.hub.mu.Lock()
			client.hub.lastHash = hash
			client.hub.mu.Unlock()
			
			log.Printf("Initial load: %s (hash: %s, size: %d bytes)", watchFile, hash[:8], len(content))
			
			fileContent := FileContent{
				Path:    watchFile,
				Content: content,
				IsDir:   false,
			}
			client.send <- fileContent
		} else {
			log.Printf("Error reading file %s: %v", filePath, err)
		}
		return
	}

	// Default behavior: walk directory but filter out Kubernetes metadata
	filepath.WalkDir(watchDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("Error walking %s: %v", path, err)
			return nil
		}

		relPath, _ := filepath.Rel(watchDir, path)
		if relPath == "." {
			return nil
		}

		var content string
		isDir := d.IsDir()

		if !isDir && isTextFile(path) {
			data, err := os.ReadFile(path)
			if err == nil {
				content = string(data)
				log.Printf("Read file %s: %d bytes", relPath, len(content))
			} else {
				log.Printf("Error reading file %s: %v", path, err)
			}
		}

		fileContent := FileContent{
			Path:    relPath,
			Content: content,
			IsDir:   isDir,
		}

		log.Printf("Sending file: %s (isDir: %v, contentLen: %d)", relPath, isDir, len(content))
		client.send <- fileContent

		return nil
	})
}

func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	textExts := []string{".txt", ".md", ".go", ".js", ".html", ".css", ".json", ".yaml", ".yml", ".xml", ".log"}

	for _, textExt := range textExts {
		if ext == textExt {
			return true
		}
	}
	return false
}

func watchFiles(hub *Hub) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("Error creating watcher:", err)
	}
	defer watcher.Close()

	err = filepath.WalkDir(watchDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})

	if err != nil {
		log.Fatal("Error adding directories to watcher:", err)
	}

	log.Printf("Watching directory: %s", watchDir)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// Thanos pattern: fsnotify sometimes sends events without name or operation
			if event.Name == "" {
				log.Printf("Received empty event, ignoring")
				continue
			}

			// For ConfigMap debugging, let's see all events first
			log.Printf("Raw event: %s %s", event.Op, event.Name)
			
			// Handle ConfigMap symlink updates - watch for ..data changes
			if strings.Contains(event.Name, "..data") {
				log.Printf("ConfigMap symlink update detected: %s", event.Name)
				hub.refresh()
				continue
			}
			
			// Thanos pattern: Everything but a CHMOD requires rereading  
			// But for now, let's be more permissive to debug ConfigMap issues
			if event.Op == fsnotify.Chmod {
				log.Printf("Ignoring CHMOD event: %s %s", event.Op, event.Name)
				continue
			}

			relPath, err := filepath.Rel(watchDir, event.Name)
			if err != nil {
				continue
			}

			// If watching a specific file, only process that file
			watchFile := getWatchFile()
			if watchFile != "" && relPath != watchFile {
				continue
			}

			log.Printf("File event: %s %s", event.Op, relPath)

			// Thanos pattern: "The most reliable solution is to reload everything if anything happens"
			hub.refresh()

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("Watcher error:", err)
		}
	}
}


func main() {
	if err := os.MkdirAll(watchDir, 0755); err != nil {
		log.Fatal("Error creating watch directory:", err)
	}

	hub := newHub()
	go hub.run()
	go watchFiles(hub)

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		wsHandler(hub, w, r)
	})

	http.HandleFunc("/", indexHandler)

	fmt.Printf("Server starting on :7070\n")
	fmt.Printf("Watching directory: %s\n", watchDir)
	if getWatchFile() != "" {
		fmt.Printf("Watching specific file: %s\n", getWatchFile())
	}
	fmt.Printf("Open http://localhost:7070 in your browser\n")

	log.Fatal(http.ListenAndServe(":7070", nil))
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := `<!DOCTYPE html>
<html>
<head>
    <title>ConfigMap Viewer</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; }
        .file-list { border: 1px solid #ddd; padding: 20px; margin: 20px 0; }
        .file-item { margin: 10px 0; padding: 10px; border: 1px solid #eee; }
        .file-path { font-weight: bold; color: #333; }
        .file-content { 
            background: #f5f5f5; 
            padding: 10px; 
            margin-top: 10px; 
            white-space: pre-wrap; 
            font-family: monospace;
            max-height: 300px;
            overflow-y: auto;
        }
        .directory { background: #e8f4fd; }
        .status { padding: 10px; background: #dff0d8; border: 1px solid #d6e9c6; margin: 10px 0; }
        .header { background: #f8f9fa; padding: 15px; border-radius: 5px; margin-bottom: 20px; }
        .configmap-info { color: #495057; font-size: 14px; }
    </style>
</head>
<body>
    <div class="header">
        <h1>📁 File Watcher</h1>
        <p class="configmap-info">Monitoring: ` + func() string { 
			if f := getWatchFile(); f != "" { return f } 
			return "all files"
		}() + `</p>
    </div>
    <div class="status">
        Watching directory: ` + watchDir + `
        <br>
        Connection status: <span id="status">Connecting...</span>
    </div>
    
    <div id="file-list" class="file-list">
        <p>Loading files...</p>
    </div>

    <script>
        const ws = new WebSocket('ws://' + window.location.host + '/ws');
        const fileList = document.getElementById('file-list');
        const status = document.getElementById('status');
        const files = new Map();

        ws.onopen = function() {
            status.textContent = 'Connected';
            status.style.color = 'green';
            console.log('WebSocket connected successfully');
        };

        ws.onclose = function() {
            status.textContent = 'Disconnected';
            status.style.color = 'red';
        };

        ws.onmessage = function(event) {
            try {
                const fileData = JSON.parse(event.data);
                console.log('Raw event data:', event.data);
                console.log('Parsed file data:', fileData);
                console.log('fileData.content (lowercase):', fileData.content);
                console.log('fileData.Content (uppercase):', fileData.Content);
                console.log('Object keys:', Object.keys(fileData));
                
                if (fileData.content === '[File Removed]') {
                    files.delete(fileData.path);
                } else {
                    files.set(fileData.path, fileData);
                }
                
                console.log('Files map now has:', files.size, 'entries');
                renderFiles();
            } catch (e) {
                console.error('Error parsing message:', e, event.data);
            }
        };

        function renderFiles() {
            console.log('Rendering files:', files);
            const sortedFiles = Array.from(files.values()).sort((a, b) => {
                if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
                return a.path.localeCompare(b.path);
            });

            if (sortedFiles.length === 0) {
                fileList.innerHTML = '<p>No files found in watched directory.</p>';
                return;
            }
            
            // Debug: show raw data
            console.log('About to render', sortedFiles.length, 'files');
            sortedFiles.forEach((file, index) => {
                console.log('File ' + index + ': Path="' + file.path + '", IsDir=' + file.isDir + ', ContentLength=' + (file.content ? file.content.length : 'undefined'));
            });

            fileList.innerHTML = sortedFiles.map(file => {
                const cssClass = file.isDir ? 'file-item directory' : 'file-item';
                const icon = file.isDir ? '📁' : '📄';
                let contentDiv = '';
                
                if (!file.isDir) {
                    const content = file.content;
                    console.log('Processing file content:', typeof content, content ? content.length : 'null/undefined');
                    
                    if (content && content.length > 0) {
                        contentDiv = '<div class="file-content">' + escapeHtml(content) + '</div>';
                    } else {
                        contentDiv = '<div class="file-content" style="color: red;">Empty file (content: ' + JSON.stringify(content) + ')</div>';
                    }
                }
                
                return '<div class="' + cssClass + '">' +
                       '<div class="file-path">' + icon + ' ' + escapeHtml(file.path) + '</div>' +
                       contentDiv +
                       '</div>';
            }).join('');
        }

        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }
    </script>
</body>
</html>`

	t, err := template.New("index").Parse(tmpl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = t.Execute(w, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
