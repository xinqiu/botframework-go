// Copyright (c) 2019 Bytedance Inc.  All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package message

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	lru "github.com/hashicorp/golang-lru"
	"github.com/larksuite/botframework-go/SDK/auth"
	"github.com/larksuite/botframework-go/SDK/common"
	"github.com/larksuite/botframework-go/SDK/protocol"
)

const (
	localCacheSize = 1000000
)

var (
	LruCache *lru.Cache
)

func init() {
	var err error
	LruCache, err = lru.New(localCacheSize)
	if err != nil {
		panic(err)
	}
}

// GetImageKey: get imagekey, image_type = message
func GetImageKey(ctx context.Context, tenantKey, appID, url, path string) (string, error) {
	if url == "" && path == "" {
		return "", common.ErrImageParams.Error()
	}

	// get from cache
	var cacheKey string
	if path != "" {
		cacheKey = path
	} else {
		cacheKey = url
	}

	if v, ok := LruCache.Get(cacheKey); ok {
		imageKey := v.(string)
		if imageKey != "" {
			return imageKey, nil
		}
		LruCache.Remove(cacheKey)
	}

	// upload image
	imageType := protocol.MessageImageType
	var body *bytes.Buffer
	var contentType string
	var err error
	if path != "" {
		body, contentType, err = GenBinaryImageByPath(path, imageType)
		if err != nil {
			return "", common.ErrGenBinImageFailed.ErrorWithExtErr(err)
		}
	} else {
		body, contentType, err = GenBinaryImageByUrl(url, imageType)
		if err != nil {
			return "", common.ErrGenBinImageFailed.ErrorWithExtErr(err)
		}
	}

	rspData, err := UploadImage(ctx, tenantKey, appID, body, contentType)
	if err != nil {
		return "", err
	}

	addLruCache(cacheKey, rspData.Data.ImageKey)

	return rspData.Data.ImageKey, nil
}

func GetImageBinData(ctx context.Context, tenantKey, appID, imageKey string) ([]byte, error) {
	if appID == "" || imageKey == "" {
		return nil, common.ErrGetImageBinDataParams.ErrorWithExtStr("param is empty")
	}

	accessToken, err := auth.GetTenantAccessToken(ctx, tenantKey, appID)
	if err != nil {
		return nil, err
	}

	rspBytes, httpCode, err := common.DoHttpGetOApi(protocol.GetImagePath,
		map[string]string{"Authorization": fmt.Sprintf("Bearer %s", accessToken)},
		map[string]string{"image_key": imageKey},
	)
	if err != nil {
		return nil, common.ErrOpenApiFailed.ErrorWithExtErr(err)
	}

	if httpCode != common.HTTPCodeOK {
		return nil, common.ErrHttpCode.ErrorWithExtStr(fmt.Sprintf("httpCode[%d]httpRspBody[%s]", httpCode, string(rspBytes)))
	}

	return rspBytes, nil
}

func UploadImage(ctx context.Context, tenantKey, appID string, body *bytes.Buffer, contentType string) (*protocol.UpLoadImageResponse, error) {
	accessToken, err := auth.GetTenantAccessToken(ctx, tenantKey, appID)
	if err != nil {
		return nil, err
	}
	authorization := fmt.Sprintf("Bearer %s", accessToken)
	header := map[string]string{"Authorization": authorization, "Content-Type": contentType}

	reqURL := common.GetOpenPlatformHost() + string(protocol.UploadImagePath)
	rspBytes, _, err := common.DoHttp(common.HTTPMethodPost, reqURL, header, body)
	if err != nil {
		return nil, common.ErrOpenApiFailed.ErrorWithExtErr(err)
	}

	rspData := &protocol.UpLoadImageResponse{}
	err = json.Unmarshal(rspBytes, &rspData)
	if err != nil {
		return nil, common.ErrJsonUnmarshal.ErrorWithExtErr(err)
	}

	if rspData.Code != 0 {
		auth.CheckAndDisableTenantToken(ctx, appID, tenantKey, rspData.Code)
		return rspData, common.ErrOpenApiReturnError.ErrorWithExtStr(fmt.Sprintf("[code:%d msg:%s]", rspData.Code, rspData.Msg))
	}

	return rspData, nil

}

func GenBinaryImageByPath(path string, imageType protocol.ImageType) (*bytes.Buffer, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open file error[%v]", err)
	}
	defer file.Close()

	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	imageFile, err := writer.CreateFormFile("image", path)
	if err != nil {
		return nil, "", fmt.Errorf("create form file error[%v]", err)
	}
	_, err = io.Copy(imageFile, file)
	if err != nil {
		return nil, "", fmt.Errorf("io copy error[%v]", err)
	}

	writer.WriteField("image_type", string(imageType))
	err = writer.Close()
	if err != nil {
		return nil, "", fmt.Errorf("writer close error[%v]", err)
	}
	contentType := writer.FormDataContentType()

	return buffer, contentType, nil
}

func GenBinaryImageByUrl(url string, imageType protocol.ImageType) (*bytes.Buffer, string, error) {
	imageBytes, err := downloadImage(url)
	if err != nil {
		return nil, "", fmt.Errorf("download image error[%v]", err)
	}

	path := strings.Split(url, "/")
	name := path[0]
	if len(path) > 1 {
		name = path[len(path)-1]
	}

	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	part, err := writer.CreateFormFile("image", name)
	if err != nil {
		return nil, "", fmt.Errorf("create form file error[%v]", err)
	}
	_, err = io.Copy(part, bytes.NewReader(imageBytes))
	if err != nil {
		return nil, "", fmt.Errorf("io copy error[%v]", err)
	}

	writer.WriteField("image_type", string(imageType))
	err = writer.Close()
	if err != nil {
		return nil, "", fmt.Errorf("writer close error[%v]", err)
	}
	contentType := writer.FormDataContentType()

	return buffer, contentType, nil
}

func downloadImage(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get error[%v]", err)
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

func addLruCache(key string, value interface{}) {
	if value != "" {
		LruCache.Add(key, value)
	}
}
