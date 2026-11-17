package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// decodeBenchmarkRequestBody 限制管理台 Benchmark 请求体大小，并要求请求体只包含一个完整 JSON 值。
// maxBytes 由调用方指定，便于测试小边界；生产入口使用与推理请求相同的 64 MiB 上限。
func decodeBenchmarkRequestBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	body, err := readLimitedRequestBody(w, r, maxBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values in request body")
		}
		return err
	}
	return nil
}
