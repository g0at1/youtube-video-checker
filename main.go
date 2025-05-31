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
)

var tmpl = template.Must(template.New("index").Parse(`
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Latest YouTube Videos</title>
    <style>
        body { font-family: Arial, sans-serif; max-width: 900px; margin: 0 auto; padding: 20px; background: #f9f9f9; }
        h1 { text-align: center; margin-bottom: 24px; }
        .video-list { list-style: none; display: flex; flex-wrap: wrap; gap: 20px; padding: 0; margin: 0; }
        .video-item {
            background: #fff;
            border-radius: 8px;
            overflow: hidden;
            box-shadow: 0 2px 5px rgba(0,0,0,0.1);
            flex: 1 1 calc(50% - 20px);
            display: flex;
            flex-direction: column;
            position: relative;
        }
        .video-item img { width: 100%; height: auto; display: block; }
        .video-info { padding: 10px; flex: 1; display: flex; flex-direction: column; justify-content: space-between; }
        .video-channel { font-size: 0.875rem; color: #999; margin-bottom: 4px; }
        .video-title { font-size: 1rem; margin: 0 0 8px; color: #333; text-decoration: none; }
        .video-title:hover { text-decoration: underline; }
        .video-pub { font-size: 0.875rem; color: #666; }

        .badge {
            position: absolute;
            top: 8px;
            left: 8px;
            background: red;
            color: white;
            padding: 2px 6px;
            font-size: 0.75rem;
            border-radius: 4px;
            text-transform: uppercase;
        }
        .badge.upcoming {
            background: orange;
        }
    </style>
</head>
<body>
    <h1>Latest Videos from Specified Channels (Last 7 Days)</h1>
    <ul class="video-list">
    {{range .}}
        <li class="video-item">
            {{if eq .LiveStatus "live"}}
                <div class="badge">LIVE</div>
            {{else if eq .LiveStatus "upcoming"}}
                <div class="badge upcoming">SCHEDULED</div>
            {{end}}

            <a href="{{.URL}}" target="_blank">
                <img src="{{.Thumbnail}}" alt="Thumbnail: {{.Title}}">
            </a>
            <div class="video-info">
                <div class="video-channel">{{.Channel}}</div>
                <a class="video-title" href="{{.URL}}" target="_blank">{{.Title}}</a>
                <div class="video-pub">
                    {{if eq .LiveStatus "live"}}
                        Live from: {{.Published.Format "02 Jan 2006, 15:04"}}
                    {{else if eq .LiveStatus "upcoming"}}
                        Starts: {{.ScheduledStartTime.Format "02 Jan 2006, 15:04"}}
                    {{else}}
                        Published: {{.Published.Format "02 Jan 2006"}}
                    {{end}}
                </div>
            </div>
        </li>
    {{end}}
    </ul>
</body>
</html>
`))

func main() {
	// Load .env if present
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on environment variables")
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
