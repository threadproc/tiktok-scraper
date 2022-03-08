package tiktokscraper

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"

	log "github.com/sirupsen/logrus"
)

func (tts *TikTokScraper) initS3() error {
	var err error
	tts.awsSession, err = session.NewSession(&aws.Config{
		Region: &tts.c.AWSRegion,
		Credentials: credentials.NewStaticCredentials(
			tts.c.AWSAccessKeyID,
			tts.c.AWSSecretKey,
			"",
		),
	})
	if err != nil {
		return err
	}

	tts.s3Uploader = s3manager.NewUploader(tts.awsSession)
	tts.s3Downloader = s3manager.NewDownloader(tts.awsSession)
	tts.s3 = s3.New(tts.awsSession)

	return nil
}

func (tts *TikTokScraper) cached(key string) bool {
	_, err := tts.s3.HeadObject(&s3.HeadObjectInput{
		Bucket: &tts.c.BucketName,
		Key:    aws.String(key),
	})

	if err == nil {
		return true
	}

	aerr, ok := err.(awserr.Error)
	if !ok {
		log.WithError(err).Error("failed to HEAD object, non-AWS error")
		return false
	}

	if aerr.Code() == "NotFound" {
		return false
	}
	if err != nil {
		log.WithError(err).Error("failed to HEAD object")
		return false
	}

	return true
}

func (tts *TikTokScraper) cachedMetadata(key string) (*TikTokMeta, error) {
	isCached := tts.cached("tiktok/" + key + ".json")
	if !isCached {
		return nil, nil
	}

	// get it from the cache
	obj, err := tts.s3.GetObject(&s3.GetObjectInput{
		Bucket: &tts.c.BucketName,
		Key:    aws.String("tiktok/" + key + ".json"),
	})
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close()

	ttm := &TikTokMeta{}
	bs, err := ioutil.ReadAll(obj.Body)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(bs, ttm); err != nil {
		return nil, err
	}

	return ttm, nil
}

func (tts *TikTokScraper) cacheMetadata(key string, ttm *TikTokMeta) error {
	jsbs, err := json.Marshal(ttm)
	if err != nil {
		return err
	}
	rd := bytes.NewReader(jsbs)

	_, err = tts.s3Uploader.Upload(&s3manager.UploadInput{
		Bucket:      &tts.c.BucketName,
		Key:         aws.String("tiktok/" + key + ".json"),
		Body:        rd,
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return err
	}

	return nil
}

func (tts *TikTokScraper) cacheVideo(key string, body io.ReadCloser, ctype string) error {
	_, err := tts.s3Uploader.Upload(&s3manager.UploadInput{
		Bucket:      &tts.c.BucketName,
		Key:         aws.String("tiktok/" + key),
		Body:        body,
		ContentType: aws.String(ctype),
	})
	return err
}

func (tts *TikTokScraper) processImages(ttm *TikTokMeta) error {
	cover, err := tts.cacheImage(ttm.Video.Cover)
	if err != nil {
		return err
	}
	ttm.Video.Cover = cover

	originCover, err := tts.cacheImage(ttm.Video.OriginCover)
	if err != nil {
		return err
	}
	ttm.Video.OriginCover = originCover

	dynamicCover, err := tts.cacheImage(ttm.Video.DynamicCover)
	if err != nil {
		return err
	}
	ttm.Video.DynamicCover = dynamicCover

	avatarLarge, err := tts.cacheImage(ttm.Author.AvatarLarger)
	if err != nil {
		return err
	}
	ttm.Author.AvatarLarger = avatarLarge

	avatarMedium, err := tts.cacheImage(ttm.Author.AvatarMedium)
	if err != nil {
		return err
	}
	ttm.Author.AvatarMedium = avatarMedium

	avatarThumb, err := tts.cacheImage(ttm.Author.AvatarThumb)
	if err != nil {
		return err
	}
	ttm.Author.AvatarThumb = avatarThumb

	return nil
}

func (tts *TikTokScraper) cacheImage(url string) (string, error) {
	sum := md5.Sum([]byte(url))
	hash := hex.EncodeToString(sum[:])
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	for k, v := range tts.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	resp, err := tts.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	_, err = tts.s3Uploader.Upload(&s3manager.UploadInput{
		Bucket:      &tts.c.BucketName,
		Key:         aws.String("tiktok/img/" + hash),
		Body:        resp.Body,
		ContentType: aws.String(resp.Header.Get("Content-Type")),
	})
	if err != nil {
		return "", err
	}

	return tts.c.URL + "/tiktok/img/" + hash, nil
}
