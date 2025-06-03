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
	"sync"
	"time"

	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

type Video struct {
	VideoID            string
	Channel            string
	Title              string
	Thumbnail          string
	URL                string
	Published          time.Time
	LiveStatus         string
	ScheduledStartTime time.Time
}

var (
	cachedVideos []Video

	lastFetch time.Time

	cacheDuration = 10 * time.Minute

	mu sync.Mutex

	tmpl *template.Template
)

func main() {
	// Load .env if present
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on environment variables")
	}

	var err error
	tmpl, err = template.ParseFiles("templates/index.html")
	if err != nil {
		log.Fatalf("Error loading template: %v", err)
	}

	apiKey := os.Getenv("YOUTUBE_API_KEY")
	if apiKey == "" {
		log.Fatal("Missing YOUTUBE_API_KEY environment variable")
	}

	// Hard‐coded channel IDs
	channelIDs := []string{
		"UC3MBGrjXHkLqo0Bs4CktzpQ", // Bez Schematu
		"UCj0LLFUIn-bjKHRQ6mqCb2w", // Krzysztof M. Maj
		"UC7zHiHZaO-ftaTTUZwHJIQg", // Bez Zycia
	}

	ctx := context.Background()
	svc, err := youtube.NewService(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatalf("Error creating YouTube service: %v", err)
	}

	channelNames := make(map[string]string)
	{
		chunks := splitChunks(channelIDs, 50)
		for _, chunk := range chunks {
			resp, err := svc.Channels.List([]string{"snippet"}).
				Id(strings.Join(chunk, ",")).Do()
			if err != nil {
				log.Fatalf("Error fetching channel info: %v", err)
			}
			for _, ch := range resp.Items {
				channelNames[ch.Id] = ch.Snippet.Title
			}
		}
	}

	lastFetch = time.Now().Add(-2 * cacheDuration)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		needRefresh := time.Since(lastFetch) > cacheDuration
		mu.Unlock()

		if needRefresh {
			videos, err := fetchLatestVideos(svc, channelNames, channelIDs)
			if err != nil {
				http.Error(w, fmt.Sprintf("YouTube API error: %v", err), http.StatusInternalServerError)
				return
			}

			mu.Lock()
			cachedVideos = videos
			lastFetch = time.Now()
			mu.Unlock()
		}

		mu.Lock()
		toRender := make([]Video, len(cachedVideos))
		copy(toRender, cachedVideos)
		mu.Unlock()

		if err := tmpl.Execute(w, toRender); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func fetchLatestVideos(
	svc *youtube.Service,
	channelNames map[string]string,
	channelIDs []string,
) ([]Video, error) {
	var videos []Video

	weekAgo := time.Now().AddDate(0, 0, -7).Format(time.RFC3339)
	for _, channelID := range channelIDs {
		searchResp, err := svc.Search.List([]string{"snippet"}).
			ChannelId(channelID).
			PublishedAfter(weekAgo).
			Type("video").
			Order("date").
			MaxResults(50).
			Do()
		if err != nil {
			return nil, fmt.Errorf("fetchLatestVideos: Search.List error: %w", err)
		}

		for _, item := range searchResp.Items {
			pubTime, err := time.Parse(time.RFC3339, item.Snippet.PublishedAt)
			if err != nil {
				continue
			}
			videos = append(videos, Video{
				VideoID:    item.Id.VideoId,
				Channel:    channelNames[channelID],
				Title:      item.Snippet.Title,
				Thumbnail:  item.Snippet.Thumbnails.High.Url,
				URL:        "https://www.youtube.com/watch?v=" + item.Id.VideoId,
				Published:  pubTime,
				LiveStatus: item.Snippet.LiveBroadcastContent,
			})
		}
	}

	var upcomingIDs []string
	for _, v := range videos {
		if v.LiveStatus == "upcoming" {
			upcomingIDs = append(upcomingIDs, v.VideoID)
		}
	}

	if len(upcomingIDs) > 0 {
		batches := splitChunks(upcomingIDs, 50)
		for _, batch := range batches {
			idParam := strings.Join(batch, ",")
			videoResp, err := svc.Videos.List([]string{"liveStreamingDetails"}).
				Id(idParam).
				Do()
			if err != nil {
				log.Printf("fetchLatestVideos: Videos.List error: %v", err)
				continue
			}

			for _, item := range videoResp.Items {
				if item.LiveStreamingDetails != nil && item.LiveStreamingDetails.ScheduledStartTime != "" {
					schedUTC, err := time.Parse(time.RFC3339, item.LiveStreamingDetails.ScheduledStartTime)
					if err != nil {
						log.Printf("Cannot parse ScheduledStartTime for %s: %v", item.Id, err)
						continue
					}
					localSched := schedUTC.In(time.Local)

					for i := range videos {
						if videos[i].VideoID == item.Id {
							videos[i].ScheduledStartTime = localSched
							break
						}
					}
				}
			}
		}
	}

	sort.Slice(videos, func(i, j int) bool {
		return videos[i].Published.After(videos[j].Published)
	})

	return videos, nil
}

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
