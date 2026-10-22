// server（inference_http）：旧版 load/unload 路由的 Deprecation 响应头，引导到 model-ops。
package server

import "net/http"

// setInferenceLegacyDeprecation marks older load/unload routes; prefer POST /api/admin/model-ops.
func setInferenceLegacyDeprecation(w http.ResponseWriter) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/admin/model-ops>; rel="successor-version"`)
}
