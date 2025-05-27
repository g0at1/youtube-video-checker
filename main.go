package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

type Video struct {
	Channel   string
	Title     string
	Thumbnail string
	URL       string
	Published time.Time
}

var tmpl = template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Latest YouTube Videos</title>
    <style>
        body { font-family: Arial, sans-serif; max-width: 900px; margin: 0 auto; padding: 20px; background: #f9f9f9; }
        h1 { text-align: center; }
        .video-list { list-style: none; display: flex; flex-wrap: wrap; gap: 20px; padding: 0; margin: 0; }
        .video-item { background: #fff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 5px rgba(0,0,0,0.1); flex: 1 1 calc(50% - 20px); display: flex; flex-direction: column; }
        .video-item img { width: 100%; height: auto; display: block; }
        .video-info { padding: 10px; flex: 1; display: flex; flex-direction: column; justify-content: space-between; }
        .video-channel { font-size: 0.875rem; color: #999; margin-bottom: 4px; }
        .video-title { font-size: 1rem; margin: 0 0 8px; color: #333; text-decoration: none; }
        .video-title:hover { text-decoration: underline; }
        .video-pub { font-size: 0.875rem; color: #666; }
    </style>
</head>
<body>
    <h1>Latest Videos from Specified Channels (Last 7 Days)</h1>
    <ul class="video-list">
    {{range .}}
        <li class="video-item">
            <a href="{{.URL}}" target="_blank">
                <img src="{{.Thumbnail}}" alt="Thumbnail for {{.Title}}">
            </a>
            <div class="video-info">
                <div class="video-channel">{{.Channel}}</div>
                <a class="video-title" href="{{.URL}}" target="_blank">{{.Title}}</a>
                <div class="video-pub">Published: {{.Published.Format "Jan 2, 2006"}}</div>
            </div>
        </li>
    {{end}}
    </ul>
</body>
</html>
`))

func main() {
	// Load .env file if present
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on environment variables")
	}

	// Read API key from environment
	apiKey := os.Getenv("YOUTUBE_API_KEY")
	if apiKey == "" {
		log.Fatal("Missing YOUTUBE_API_KEY environment variable")
	}

	// Hard-coded list of channel IDs
	channelIDs := []string{
		"UC3MBGrjXHkLqo0Bs4CktzpQ", // Bez Schematu
		"UCj0LLFUIn-bjKHRQ6mqCb2w", // Krzysztof M. Maj
		"UC7zHiHZaO-ftaTTUZwHJIQg", // Bez Zycia
	}

	// Create YouTube service
	srv, err := youtube.NewService(context.Background(), option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatalf("Error creating YouTube service: %v", err)
	}

	// Fetch channel names
	channelNames := make(map[string]string)
	chunks := splitChunks(channelIDs, 50)
	for _, chunk := range chunks {
		chResp, err := srv.Channels.List([]string{"snippet"}).
			Id(strings.Join(chunk, ",")).
			Do()
		if err != nil {
			log.Fatalf("Error fetching channel info: %v", err)
		}
		for _, ch := range chResp.Items {
			channelNames[ch.Id] = ch.Snippet.Title
		}
	}

	// Calculate date one week ago in RFC3339
	weekAgo := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)

	// HTTP handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var videos []Video
		for _, channelID := range channelIDs {
			resp, err := srv.Search.List([]string{"snippet"}).
				ChannelId(channelID).
				PublishedAfter(weekAgo).
				Type("video").
				Order("date").
				MaxResults(50).
				Do()
			if err != nil {
				http.Error(w, fmt.Sprintf("YouTube API error: %v", err), http.StatusInternalServerError)
				return
			}
			for _, item := range resp.Items {
				pubTime, err := time.Parse(time.RFC3339, item.Snippet.PublishedAt)
				if err != nil {
					continue
				}
				videos = append(videos, Video{
					Channel:   channelNames[channelID],
					Title:     item.Snippet.Title,
					Thumbnail: item.Snippet.Thumbnails.High.Url,
					URL:       fmt.Sprintf("https://www.youtube.com/watch?v=%s", item.Id.VideoId),
					Published: pubTime,
				})
			}
		}
		sort.Slice(videos, func(i, j int) bool {
			return videos[i].Published.After(videos[j].Published)
		})

		if err := tmpl.Execute(w, videos); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// splitChunks splits a slice into chunks of max size n
func splitChunks(ids []string, n int) [][]string {
	var chunks [][]string
	for i := 0; i < len(ids); i += n {
		end := i + n
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[i:end])
	}
	return chunks
}
