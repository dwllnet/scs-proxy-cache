package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	/* Cache folder should be on a separate disk to avoid filling up the root disk */
	cacheDir = "./cache"
	/* Log folder should on it's on disk to avoid filling up the root disk */
	logsDir = "./logs"
	/* Remote website to cache the images from */
	remoteBaseURL = "http://real.example.com"
	/* Number of hours the image should be cached before refreshing */
	cacheExpiry = 72 * time.Hour
	/* How long to wait before checking the remote server for changes. */
	cacheRefreshTime = 72 * time.Hour
	accessLogFile    = "access.log"
	certFile         = "cert.pem" // TLS certificate file
	keyFile          = "key.key"
)

var (
	cacheMutex = sync.RWMutex{} // Protect access to cache metadata
	logger     *log.Logger
)

func main() {
	// Ensure cache and logs directories exist
	createDirIfNotExist(cacheDir)
	createDirIfNotExist(logsDir)

	// Set up logging
	logFile, err := os.OpenFile(filepath.Join(logsDir, accessLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()
	logger = log.New(logFile, "", log.LstdFlags)

	// Configure HTTPS server
	server := &http.Server{
		Addr: ":443",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleRequest(w, r)
		}),
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	fmt.Println("Cache server running...")

	err = server.ListenAndServeTLS(certFile, keyFile)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	requestedPath := r.URL.Path
	ip := r.RemoteAddr

	cachedFilePath := ""
	
	cachedFilePath = filepath.Join(cacheDir, filepath.Clean(requestedPath))

	logger.Printf("%s incoming request: %s", ip, requestedPath)

	// Check if file exists in cache
	cacheMutex.RLock()
	fileInfo, err := os.Stat(cachedFilePath)
	cacheMutex.RUnlock()

	if err == nil && time.Since(fileInfo.ModTime()) < cacheExpiry {
		// Serve from cache
		logger.Printf("%s serving from cache: %s", ip, requestedPath)
		http.ServeFile(w, r, cachedFilePath)
		go refreshCacheAsync(requestedPath, cachedFilePath, fileInfo.ModTime())
		return
	}

	// Fetch from remote server
	err = fetchAndCache(requestedPath, cachedFilePath)
	if err != nil {
		http.Error(w, "Unable to fetch the requested file", http.StatusInternalServerError)
		logger.Printf("%s Failed to fetch file: %s, error: %v", ip, requestedPath, err)
		return
	}

	// Serve the newly cached file
	logger.Printf("%s serving newly fetched file: %s", ip, requestedPath)
	http.ServeFile(w, r, cachedFilePath)
}

func fetchAndCache(requestedPath, cachedFilePath string) error {
	remoteURL := remoteBaseURL + requestedPath

	tlsConf, err := skipVerification()
	if err != nil {
		log.Fatalf("Error creating TLS configuration: %v", err)
	}

	client := &http.Client{
		Timeout:   time.Second * 50,
		Transport: &http.Transport{TLSClientConfig: tlsConf},
	}
	resp, err := client.Get(remoteURL)
	if err != nil {
		return fmt.Errorf("failed to fetch from remote: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote server returned non-200 status code: %d", resp.StatusCode)
	}

	// Write to cache
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	err = os.MkdirAll(filepath.Dir(cachedFilePath), os.ModePerm)
	if err != nil {
		return fmt.Errorf("failed to create cache directories: %w", err)
	}

	outFile, err := os.Create(cachedFilePath)
	if err != nil {
		return fmt.Errorf("failed to create cached file: %w", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write to cached file: %w", err)
	}

	return nil
}

func refreshCacheAsync(requestedPath, cachedFilePath string, lastModifiedTime time.Time) {
	// Refresh cache only if 24 hours have passed since the last modification
	if time.Since(lastModifiedTime) < cacheRefreshTime {
		return
	}

	logger.Printf("Refreshing cache asynchronously: %s", requestedPath)

	err := fetchAndCache(requestedPath, cachedFilePath)
	if err != nil {
		logger.Printf("Failed to refresh cache for: %s, error: %v", requestedPath, err)
	}
}

func createDirIfNotExist(dir string) {
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		log.Fatalf("Failed to create directory %s: %v", dir, err)
	}
}

func skipVerification() (*tls.Config, error) {
	return &tls.Config{InsecureSkipVerify: true}, nil
}
