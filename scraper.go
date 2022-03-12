package tiktokscraper

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
	"go.yhsif.com/rowlock"
)

var defaultHeaders = map[string]string{
	"Accept":     "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9",
	"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:97.0) Gecko/20100101 Firefox/97.0",
}

type TikTokScraper struct {
	httpClient   *http.Client
	r            *resty.Client
	cookies      map[string]string
	c            *TikTokScraperConfig
	awsSession   *session.Session
	s3Uploader   *s3manager.Uploader
	s3Downloader *s3manager.Downloader
	s3           *s3.S3
	lock         *rowlock.RowLock
}

type TikTokMeta struct {
	ID          string `json:"id"`
	Description string `json:"desc"`
	CreateTime  int64  `json:"createTime"`
	Video       struct {
		Height       int    `json:"height"`
		Width        int    `json:"width"`
		Duration     int    `json:"duration"`
		Cover        string `json:"cover"`
		OriginCover  string `json:"originCover"`
		DynamicCover string `json:"dynamicCover"`
		PlayAddr     string `json:"playAddr"`
		DownloadAddr string `json:"downloadAddr"`
		Format       string `json:"format"`
	} `json:"video"`
	Author struct {
		ID           string `json:"id"`
		UniqueID     string `json:"uniqueId"`
		Nickname     string `json:"nickname"`
		AvatarLarger string `json:"avatarLarger"`
		AvatarMedium string `json:"avatarMedium"`
		AvatarThumb  string `json:"avatarThumb"`
		Signature    string `json:"signature"`
	} `json:"author"`
	Stats struct {
		DiggCount    int `json:"diggCount"`
		ShareCount   int `json:"shareCount"`
		CommentCount int `json:"commentCount"`
		PlayCount    int `json:"playCount"`
	} `json:"stats"`

	CDNVideoURL string `json:"cdnVideoURL"`
}

type tikTokAPIResponse struct {
	StatusCode    int    `json:"statusCode"`
	StatusMessage string `json:"statusMsg"`
	ItemInfo      struct {
		ItemStruct *TikTokMeta `json:"itemStruct"`
	} `json:"itemInfo"`
}

type TikTokScraperConfig struct {
	BucketName     string
	URL            string
	AWSAccessKeyID string
	AWSSecretKey   string
	AWSRegion      string
	SentryDSN      string
	Environment    string
}

func NewScraper(c *TikTokScraperConfig) (*TikTokScraper, error) {
	tts := &TikTokScraper{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		cookies: make(map[string]string),
		c:       c,
		lock:    rowlock.NewRowLock(rowlock.MutexNewLocker),
	}
	tts.r = resty.NewWithClient(tts.httpClient)

	for k, v := range defaultHeaders {
		tts.r.SetHeader(k, v)
	}

	// init the cookies
	if err := tts.setInitialCookies(); err != nil {
		return nil, err
	}

	if err := tts.initS3(); err != nil {
		return nil, err
	}

	return tts, nil
}

func (tts *TikTokScraper) setInitialCookies() error {
	req, err := http.NewRequest("GET", "https://www.tiktok.com", nil)
	if err != nil {
		return err
	}
	for k, v := range defaultHeaders {
		req.Header.Set(k, v)
	}

	resp, err := tts.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {

		bs, _ := ioutil.ReadAll(resp.Body)
		log.Warn(string(bs))

		return fmt.Errorf("could not get cookies from tiktok, status code = %d", resp.StatusCode)
	}

	// save our cookies please
	for _, cookie := range resp.Cookies() {
		log.Info("Found cookie ", cookie.Name, " = ", cookie.Value)
		tts.cookies[cookie.Name] = cookie.Value
	}

	return nil
}

func (tts *TikTokScraper) ResolveHash(hash string) (string, string, error) {
	// do a redirecting head request
	ttURL := "https://vm.tiktok.com/" + hash

	resp, err := tts.httpClient.Head(ttURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "", "", nil
	}

	dest := resp.Request.URL

	if dest.Hostname() != "www.tiktok.com" && dest.Hostname() != "tiktok.com" {
		return "", "", errors.New("not a valid tiktok URL in response")
	}

	destParts := strings.Split(strings.Trim(dest.Path, "/"), "/")
	if len(destParts) != 3 || destParts[1] != "video" {
		return "", "", errors.New("invalid tiktok URL in response")
	}

	return destParts[0], destParts[2], nil
}

func (tts *TikTokScraper) ScrapeVideo(username, videoID string) (*TikTokMeta, error) {
	// try to avoid some basic attempts to get anything from the cache
	if strings.Contains(videoID, "/") || strings.Contains(username, "/") {
		return nil, nil
	}

	// attempt to get the metadata from the cache
	cacheKey := fmt.Sprintf("%s/%s", username, videoID)

	// this will help prevent fetching metadata multiple times for the same video
	// which will keep our traffic to tiktok + aws down
	tts.lock.Lock(cacheKey)
	defer tts.lock.Unlock(cacheKey)

	cachedMeta, err := tts.cachedMetadata(cacheKey)
	if err != nil {
		return nil, err
	}
	if cachedMeta != nil {
		log.Info("Returning cached metadata for ", cacheKey)
		return cachedMeta, nil
	}

	log.Info("Getting metadata from TikTok for ", cacheKey)

	ttURL := fmt.Sprintf("https://www.tiktok.com/node/share/video/%s/%s", username, videoID)

	cookies := make([]*http.Cookie, 0)
	for k, v := range tts.cookies {
		cookies = append(cookies, &http.Cookie{Name: k, Value: v})
	}

	// TODO: we should use sessions at some point here, just to make sure that we
	// do not like, get banned from the TikTok API lol
	resp, err := tts.r.R().SetResult(&tikTokAPIResponse{}).SetCookies(cookies).Get(ttURL)
	if err != nil {
		return nil, err
	}

	res, ok := resp.Result().(*tikTokAPIResponse)
	if !ok {
		return nil, errors.New("failed to unmarshal response into struct")
	}

	if res.StatusCode == 404 {
		return nil, nil
	}

	if res.StatusCode != 0 {
		return nil, fmt.Errorf("tiktok api response code %d: %s", res.StatusCode, res.StatusMessage)
	}

	// do the scraping of the video
	if err := tts.ScrapeVideoClip(cacheKey, res.ItemInfo.ItemStruct); err != nil {
		return nil, err
	}

	// cache all the images in the request
	if err := tts.processImages(res.ItemInfo.ItemStruct); err != nil {
		return nil, err
	}

	// we want to cache this in S3
	if err := tts.cacheMetadata(cacheKey, res.ItemInfo.ItemStruct); err != nil {
		return nil, err
	}

	return res.ItemInfo.ItemStruct, nil
}

func (tts *TikTokScraper) ScrapeVideoClip(cacheKey string, ttm *TikTokMeta) error {
	// we've already done it
	if ttm.CDNVideoURL != "" {
		return nil
	}

	// do the do
	addr := ttm.Video.DownloadAddr
	if addr == "" {
		addr = ttm.Video.PlayAddr
	}

	log.Info("Downloading video for ", cacheKey)

	referer := "https://www.tiktok.com/@" + ttm.Author.UniqueID + "/video/" + ttm.ID

	req, err := http.NewRequest("GET", addr, nil)
	if err != nil {
		return err
	}
	for k, v := range tts.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	req.Header.Set("Referer", referer)

	resp, err := tts.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	err = tts.cacheVideo(cacheKey+"."+ttm.Video.Format, resp.Body, resp.Header.Get("Content-Type"))
	if err != nil {
		return err
	}

	ttm.CDNVideoURL = tts.c.URL + "/tiktok/" + cacheKey + "." + ttm.Video.Format

	return nil
}
