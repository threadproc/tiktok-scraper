package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/BurntSushi/toml"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	tiktokscraper "github.com/threadproc/tiktok-scraper"
)

var tts *tiktokscraper.TikTokScraper

type ttResponse struct {
	Error string                    `json:"error,omitempty"`
	Video *tiktokscraper.TikTokMeta `json:"video,omitempty"`
}

func errResponse(c *gin.Context, code int, err error) {
	log.WithError(err).Error("error in response")
	c.JSON(code, &ttResponse{
		Error: err.Error(),
	})
}

func handleVideoRequest(c *gin.Context) {
	username := c.Param("username")
	videoID := c.Param("videoid")

	meta, err := tts.ScrapeVideo(username, videoID)
	if err != nil {
		// we should check to see if this is a known error first
		errResponse(c, 500, err)
		return
	}
	if meta == nil {
		errResponse(c, 404, errors.New("video not found"))
		return
	}

	c.JSON(200, &ttResponse{
		Video: meta,
	})
}

func handleShortURL(c *gin.Context) {
	username, videoID, err := tts.ResolveHash(c.Param("hash"))
	if err != nil {
		// return an error response
		errResponse(c, 500, err)
		return
	}

	if len(username) == 0 || len(videoID) == 0 {
		errResponse(c, 404, errors.New("could not find video by hash"))
		return
	}

	url := fmt.Sprintf("/video/%s/%s", username, videoID)
	c.Redirect(http.StatusMovedPermanently, url)
}

func main() {
	log.Info("🚀 Starting tiktok-scraper-lambda")

	c := &tiktokscraper.TikTokScraperConfig{}
	if _, err := toml.DecodeFile("config.toml", c); err != nil {
		log.WithError(err).Fatal("could not load config file")
	}

	var err error
	tts, err = tiktokscraper.NewScraper(c)
	if err != nil {
		log.WithError(err).Fatal("failed to initialize tiktok scraper")
	}

	r := gin.Default()

	r.GET("/hash/:hash", handleShortURL)
	r.GET("/video/:username/:videoid", handleVideoRequest)

	if err := http.ListenAndServe("0.0.0.0:8082", r); err != nil {
		log.WithError(err).Fatal("could not listen and serve")
	}
}
