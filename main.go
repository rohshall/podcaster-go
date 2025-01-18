package main

import (
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

type (
	RSS struct {
		Channel Channel `xml:"channel"`
	}

	Channel struct {
		Title string `xml:"title"`
		Items []Item `xml:"item"`
	}

	Enclosure struct {
		Url string `xml:"url,attr"`
	}

	Item struct {
		Title     string    `xml:"title"`
		Enclosure Enclosure `xml:"enclosure"`
	}

	Podcast struct {
		Id  string `json:"id"`
		Url string `json:"url"`
	}
	// Config represents the configuration for the podcaster application.
	Config struct {
		MediaDir string    `json:"media_dir"`
		Podcasts []Podcast `json:"podcasts"`
	}
	// State represents the state of the podcaster application.
	State struct {
		Downloaded []string `json:"downloaded"`
		// Add more fields as needed
	}
	// DownloadTask represents a task to download a file from a URL and save it to a specified path.
	DownloadTask struct {
		Title      string
		Url        string
		OutputPath string
	}
)

func main() {
	// Define and parse command-line flags
	pid := flag.String("pid", "", "ID of the podcast to download")
	count := flag.Int("count", 1, "Number of episodes to download")
	flag.Parse()

	config, err := getConfig()
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}
	// Create or load state
	stateFile := filepath.Join(config.MediaDir, "podcaster-state.json")
	state, err := loadState(stateFile)
	if err != nil {
		// Handle error or initialize default state
		state = State{Downloaded: make([]string, 0)}
	}

	var podcasts []Podcast
	for _, podcast := range config.Podcasts {
		if *pid != "" && *pid != podcast.Id {
			continue
		}
		podcasts = append(podcasts, podcast)
	}
	// Create a wait group and the result channel
	var downloadTasksWg sync.WaitGroup
	errChan := make(chan error)
	// Create a channel to receive the downloadTasksChan - paths of the downloaded episodes.
	downloadTasksChan := make(chan DownloadTask)
	var httpClient = &http.Client{}
	var downloadedFilesWg sync.WaitGroup
	downloadedFilesChan := make(chan string)
	// Download each podcast in parallel
	for _, podcast := range podcasts {
		downloadTasksWg.Add(1)
		outputDir := filepath.Join(config.MediaDir, podcast.Id)
		go downloadPodcast(httpClient, podcast, outputDir, *count, state, &downloadTasksWg, downloadTasksChan, errChan)
	}
	go func() {
		for downloadTask := range downloadTasksChan {
			downloadedFilesWg.Add(1)
			go downloadFile(httpClient, downloadTask, &downloadedFilesWg, downloadedFilesChan, errChan)
		}
	}()
	go func() {
		// Update state with downloaded episodes
		for downloadedFile := range downloadedFilesChan {
			state.Downloaded = append(state.Downloaded, downloadedFile)
		}
	}()
	go func() {
		// Handle errors
		for err := range errChan {
			log.Println(err)
		}
	}()

	// Wait for all downloads to complete
	downloadTasksWg.Wait()
	close(downloadTasksChan)
	downloadedFilesWg.Wait()
	close(downloadedFilesChan)
	close(errChan)

	// Save state
	err = saveState(state, stateFile)
	if err != nil {
		log.Fatalf("Failed to save state: %v", err)
	}

	fmt.Println("All downloads completed.")
}

func downloadPodcast(httpClient *http.Client, podcast Podcast, outputDir string, count int, state State, wg *sync.WaitGroup, downloadTasksChan chan<- DownloadTask, errChan chan<- error) {
	defer wg.Done()

	err := os.MkdirAll(outputDir, os.ModePerm)
	if err != nil {
		errChan <- fmt.Errorf("failed to create directory: %v", err)
		return
	}
	fmt.Printf("Fetching RSS feed for %s from \"%s\"...\n", podcast.Id, podcast.Url)
	// Fetch and parse the RSS feed
	rss, err := fetchRSSFeed(httpClient, podcast.Url)
	if err != nil {
		errChan <- fmt.Errorf("failed to fetch RSS feed \"%s\": %v", podcast.Url, err)
		return
	}

	if len(rss.Channel.Items) == 0 {
		errChan <- fmt.Errorf("no episodes found in the RSS feed \"%s\"", podcast.Url)
		return
	}

	// Get the latest episodes
	for i := 0; i < count && i < len(rss.Channel.Items); i++ {
		episode := rss.Channel.Items[i]

		// Extract the file name from the URL
		// Parse the URL
		parsedURL, err := url.Parse(strings.TrimSpace(episode.Enclosure.Url))
		if err != nil {
			errChan <- fmt.Errorf("failed to parse URL \"%s\": %v", episode.Enclosure.Url, err)
			continue
		}

		// Get the path without query parameters
		fileName := filepath.Base(parsedURL.Path)
		if fileName == "" {
			errChan <- fmt.Errorf("failed to determine the file name from the episode URL: \"%s\"", parsedURL.Path)
			continue
		}

		// Construct the full output path
		outputPath := filepath.Join(outputDir, fileName)

		if slices.Contains(state.Downloaded, outputPath) {
			fmt.Printf("Episode \"%s\" was already downloaded: \"%s\"\n", episode.Title, outputPath)
			continue
		}

		downloadTask := DownloadTask{
			Title:      episode.Title,
			Url:        episode.Enclosure.Url,
			OutputPath: outputPath,
		}
		downloadTasksChan <- downloadTask
	}
}

// downloadFile downloads a file from the given URL and saves it to the specified path.
func downloadFile(httpClient *http.Client, downloadTask DownloadTask, wg *sync.WaitGroup, downloadedFilesChan chan<- string, errChan chan<- error) {
	defer wg.Done()

	if _, err := os.Stat(downloadTask.OutputPath); err == nil {
		fmt.Printf("Episode \"%s\" already downloaded: \"%s\"\n", downloadTask.Title, downloadTask.OutputPath)
		downloadedFilesChan <- downloadTask.OutputPath
		return
	}

	fmt.Printf("Downloading the episode \"%s\" to \"%s\"...\n", downloadTask.Title, downloadTask.OutputPath)

	resp, err := httpClient.Get(downloadTask.Url)
	if err != nil {
		errChan <- fmt.Errorf("failed to fetch URL \"%s\": %v", downloadTask.Url, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errChan <- fmt.Errorf("failed to fetch URL \"%s\"; got HTTP status: %s", downloadTask.Url, resp.Status)
		return
	}

	file, err := os.Create(downloadTask.OutputPath)
	if err != nil {
		errChan <- fmt.Errorf("failed to create file \"%s\": %v", downloadTask.OutputPath, err)
		return
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		errChan <- fmt.Errorf("failed to write to file \"%s\": %v", downloadTask.OutputPath, err)
		return
	}

	downloadedFilesChan <- downloadTask.OutputPath
	fmt.Printf("Successfully downloaded episode \"%s\" to \"%s\"\n", downloadTask.Title, downloadTask.OutputPath)
}

// fetchRSSFeed fetches and parses the RSS feed from the given URL.
func fetchRSSFeed(httpClient *http.Client, feedURL string) (*RSS, error) {
	resp, err := httpClient.Get(feedURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch RSS feed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	var rss RSS
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&rss); err != nil {
		return nil, fmt.Errorf("failed to parse RSS feed: %v", err)
	}

	return &rss, nil
}

func getDefaultConfigPath() string {
	// Determine the default config path based on the OS
	if configPath, found := os.LookupEnv("XDG_CONFIG_HOME"); found {
		return filepath.Join(configPath, "podcaster", "config.json")
	} else if homeDir, found := os.LookupEnv("HOME"); found { // For Linux/macOS
		return filepath.Join(homeDir, ".config", "podcaster", "config.json")
	} else if appData, found := os.LookupEnv("APPDATA"); found { // For Windows
		return filepath.Join(appData, "podcaster", "config.json")
	}
	// Fallback to the current directory
	return "config.json"
}

func loadState(filename string) (State, error) {
	var state State
	data, err := os.ReadFile(filename)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(data, &state)
	return state, err
}

func saveState(state State, filename string) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func getConfig() (Config, error) {
	// Get the default config path
	configPath := getDefaultConfigPath()
	fmt.Printf("Using config file: %s\n", configPath)

	configFile, err := os.Open(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("failed to open config file: %v", err)
	}
	defer configFile.Close()

	var config Config
	decoder := json.NewDecoder(configFile)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("failed to parse config file: %v", err)
	}
	return config, nil
}
