#!/usr/bin/env python3
"""检查高风险管理 API 的 OpenAPI Schema 与 Go Handler 契约。

这是分阶段字段级门禁，不替代完整 OpenAPI/Go 类型等价性验证。
只使用 Python 标准库。
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OPENAPI_FILE = ROOT / "docs" / "openapi.yaml"
PROVIDER_HANDLER_FILE = ROOT / "internal" / "server" / "admin_providers_http.go"
REQUEST_STATS_FILE = ROOT / "internal" / "server" / "request_stats.go"
MODEL_STRUCT_FILE = ROOT / "internal" / "model" / "model.go"
GATEWAY_MODEL_OPS_FILE = ROOT / "internal" / "server" / "gateway_model_ops.go"
STORE_REQUEST_LOG_FILE = ROOT / "internal" / "store" / "store.go"
REQUEST_DETAIL_SOURCE_FILE = ROOT / "internal" / "server" / "request_detail.go"
REQUEST_ANALYSIS_FILE = ROOT / "internal" / "server" / "request_analysis.go"
AUDIT_METRICS_FILE = ROOT / "internal" / "audit" / "metrics.go"
REQUEST_RETENTION_FILE = ROOT / "internal" / "server" / "request_retention.go"
REQUEST_LIST_HANDLER_FILE = ROOT / "internal" / "server" / "server.go"
REQUEST_CANCEL_HANDLER_FILE = ROOT / "internal" / "server" / "pipeline_http.go"
AUTH_HANDLER_FILE = ROOT / "internal" / "server" / "admin_auth_http.go"
DOWNLOAD_DISMISS_HANDLER_FILE = ROOT / "internal" / "server" / "download_dismiss.go"
OLLAMA_HANDLER_FILE = ROOT / "internal" / "server" / "ollama_admin.go"
CATALOG_HANDLER_FILE = ROOT / "internal" / "server" / "admin_catalog_http.go"
RUNTIME_TYPES_FILE = ROOT / "internal" / "llama" / "runtime.go"
OLLAMA_TAG_TYPES_FILE = ROOT / "internal" / "catalog" / "ollama" / "tags.go"
INFERENCE_OPS_FILE = ROOT / "internal" / "server" / "inference_ops.go"


def schema_block(source: str, name: str) -> str:
    schemas_start = source.find("\n  schemas:")
    if schemas_start < 0:
        raise ValueError("docs/openapi.yaml: components.schemas section not found")
    tail = source[schemas_start:]
    match = re.search(rf"(?m)^    {re.escape(name)}:[ \t]*$", tail)
    if not match:
        raise ValueError(f"OpenAPI schema {name!r} not found")
    body = tail[match.end():]
    next_schema = re.search(r"(?m)^    [A-Za-z][A-Za-z0-9_-]*:[ \t]*$", body)
    return body[:next_schema.start()] if next_schema else body


def schema_field_block(schema: str, field: str) -> str:
    match = re.search(
        rf"(?m)^        {re.escape(field)}:[ \t]*(?:\{{[^\n]*\}})?[ \t]*$",
        schema,
    )
    if not match:
        raise ValueError(f"OpenAPI schema is missing property {field!r}")
    tail = schema[match.end():]
    next_field = re.search(r"(?m)^        [A-Za-z][A-Za-z0-9_-]*:", tail)
    return match.group(0) + (tail[:next_field.start()] if next_field else tail)


def path_block(source: str, path: str) -> str:
    paths_section = source.split("\ncomponents:", 1)[0]
    match = re.search(rf"(?m)^  {re.escape(path)}:[ \t]*$", paths_section)
    if not match:
        raise ValueError(f"OpenAPI path {path!r} not found")
    tail = paths_section[match.end():]
    next_path = re.search(r"(?m)^  /[^\n]*:[ \t]*$", tail)
    return tail[:next_path.start()] if next_path else tail


def admin_api_key_scope_errors(source: str, auth_handler: str) -> list[str]:
    """Ensure the deployment Admin API Key is advertised only for service-key management."""
    errors: list[str] = []
    paths_section = source.split("\ncomponents:", 1)[0]

    def check_operation(path: str, method: str, body: str) -> None:
        key_allowed = path == "/api/admin/keys" or path.startswith("/api/admin/keys/")
        public_auth_paths = {"/api/auth/status", "/api/auth/login", "/api/auth/logout"}
        if path.startswith("/api/") and path not in public_auth_paths and "AdminSessionBearer: []" not in body:
            errors.append(
                f"{method.upper()} {path} must declare AdminSessionBearer; only auth status/login/logout are public"
            )
        if not key_allowed and re.search(r"(?m)^\s*-\s*AdminKey:\s*\[\s*\]\s*$", body):
            errors.append(
                f"{method.upper()} {path} must not advertise AdminKey; the deployment Admin API Key is scoped to /api/admin/keys*"
            )
        if not key_allowed and "configured administrator credential" in body:
            errors.append(
                f"{method.upper()} {path} must require a valid admin web session, not the deployment Admin API Key"
            )

    path_matches = list(re.finditer(r"(?m)^  (/[^:\n]+):[ \t]*$", paths_section))
    for path_index, path_match in enumerate(path_matches):
        path = path_match.group(1)
        path_end = path_matches[path_index + 1].start() if path_index + 1 < len(path_matches) else len(paths_section)
        path_body = paths_section[path_match.end():path_end]
        method_matches = list(re.finditer(r"(?m)^    (get|post|put|delete|patch|options|head):[ \t]*$", path_body))
        for method_index, method_match in enumerate(method_matches):
            method_end = method_matches[method_index + 1].start() if method_index + 1 < len(method_matches) else len(path_body)
            body = path_body[method_match.end():method_end]
            check_operation(path, method_match.group(1), body)

    components_match = re.search(r"(?m)^components:[ \t]*$", source)
    if components_match:
        components = source[components_match.end():]
        path_items_match = re.search(r"(?m)^  pathItems:[ \t]*$", components)
        if path_items_match:
            path_items = components[path_items_match.end():]
            next_section = re.search(r"(?m)^  [A-Za-z][A-Za-z0-9_-]*:[ \t]*$", path_items)
            if next_section:
                path_items = path_items[:next_section.start()]
            item_matches = list(re.finditer(r"(?m)^    ([A-Za-z][A-Za-z0-9_-]*):[ \t]*$", path_items))
            for item_index, item_match in enumerate(item_matches):
                item_name = item_match.group(1)
                item_end = item_matches[item_index + 1].start() if item_index + 1 < len(item_matches) else len(path_items)
                item_body = path_items[item_match.end():item_end]
                method_matches = list(re.finditer(r"(?m)^      (get|post|put|delete|patch):[ \t]*$", item_body))
                for method_index, method_match in enumerate(method_matches):
                    method_end = method_matches[method_index + 1].start() if method_index + 1 < len(method_matches) else len(item_body)
                    operation_body = item_body[method_match.end():method_end]
                    check_operation(
                        f"components.pathItems.{item_name}",
                        method_match.group(1),
                        operation_body,
                    )
                    if "AdminSessionBearer: []" not in operation_body:
                        errors.append(
                            f"components.pathItems.{item_name} {method_match.group(1).upper()} must require AdminSessionBearer at operation level"
                        )
    key_scope_function = go_function(auth_handler, "func adminAPIKeyAllowedForPath(")
    key_scope_function = re.sub(r"(?m)^\s*//.*$", "", key_scope_function)
    compact_scope = " ".join(key_scope_function.split())
    expected_scope = 'func adminAPIKeyAllowedForPath(path string) bool { return path == "/api/admin/keys" || strings.HasPrefix(path, "/api/admin/keys/") }'
    if compact_scope != expected_scope:
        errors.append("adminAPIKeyAllowedForPath must allow only /api/admin/keys and its descendants")

    authorized_function = go_function(auth_handler, "func (s *Server) adminAuthorized(")
    if "adminAPIKeyAllowedForPath(r.URL.Path)" not in authorized_function:
        errors.append("adminAuthorized must enforce the narrow admin API Key path allowlist")
    if "subtle.ConstantTimeCompare" not in authorized_function:
        errors.append("adminAuthorized must compare the admin API Key in constant time")
    return errors


def operation_block(source: str, path: str, method: str) -> str:
    body = path_block(source, path)
    match = re.search(rf"(?m)^    {re.escape(method.lower())}:[ \t]*$", body)
    if not match:
        raise ValueError(f"OpenAPI operation {method.upper()} {path} not found")
    tail = body[match.end():]
    next_method = re.search(
        r"(?m)^    (?:get|post|put|patch|delete|options|head|trace):[ \t]*$",
        tail,
    )
    return tail[:next_method.start()] if next_method else tail


def response_block(operation: str, status: str) -> str:
    match = re.search(rf'(?m)^        "{re.escape(status)}":[ \t]*$', operation)
    if not match:
        raise ValueError(f"OpenAPI operation is missing HTTP {status} response")
    tail = operation[match.end():]
    next_response = re.search(r'(?m)^        "\d{3}":[ \t]*$', tail)
    return tail[:next_response.start()] if next_response else tail


def go_function(source: str, signature: str) -> str:
    start = source.find(signature)
    if start < 0:
        raise ValueError(f"Go implementation function {signature!r} not found")
    end = source.find("\nfunc ", start + len(signature))
    return source[start:] if end < 0 else source[start:end]


def validate(
    source: str,
    provider_handler: str,
    stats_handler: str,
    model_struct: str,
    gateway_model_ops: str,
    request_list_handler: str,
    request_cancel_handler: str,
    store_request_log: str,
    request_detail_source: str,
    request_analysis_source: str,
    audit_metrics_source: str,
    request_retention_source: str,
    auth_handler: str,
    download_dismiss_handler: str,
    ollama_handler: str,
    catalog_handler: str,
    runtime_types: str,
    ollama_tag_types: str,
    inference_ops: str,
) -> list[str]:
    errors: list[str] = []

    def require(condition: bool, message: str) -> None:
        if not condition:
            errors.append(message)

    try:
        mutation = schema_block(source, "ProviderMutation")
        view = schema_block(source, "ProviderView")
        mutation_fields = {
            "max_concurrent_requests",
            "max_queue_size",
            "queue_wait_timeout_seconds",
            "requests_per_minute",
            "token_budget_per_day",
            "request_timeout_seconds",
        }
        view_fields = mutation_fields | {
            "queued_requests",
            "queue_limit",
            "queue_timeouts",
            "concurrent_requests_used",
            "concurrent_requests_limit",
            "concurrent_requests_remaining",
            "concurrent_requests_rejected",
            "concurrent_requests_usage_available",
            "token_budget_per_day_used",
            "token_budget_per_day_remaining",
            "token_budget_per_day_reset_seconds",
            "token_budget_per_day_usage_available",
        }
        for field in sorted(mutation_fields):
            try:
                schema_field_block(mutation, field)
            except ValueError as exc:
                errors.append(f"ProviderMutation: {exc}")
        for field in sorted(view_fields):
            try:
                schema_field_block(view, field)
            except ValueError as exc:
                errors.append(f"ProviderView: {exc}")

        for obsolete_field in ("source", "editable", "source_conflict", "source_override", "deleted"):
            try:
                schema_field_block(view, obsolete_field)
            except ValueError:
                pass
            else:
                errors.append(
                    f"ProviderView still exposes obsolete lifecycle field {obsolete_field!r}; "
                    "all remote Providers are Web-managed"
                )

        backend_result = schema_block(source, "BackendSwitchResult")
        try:
            available_backend_fields = schema_field_block(backend_result, "available_backends")
        except ValueError as exc:
            errors.append(f"BackendSwitchResult: {exc}")
        else:
            for obsolete_field in ("source", "editable", "source_conflict", "source_override", "deleted"):
                if re.search(rf"(?m)^\s+{re.escape(obsolete_field)}:", available_backend_fields):
                    errors.append(
                        f"BackendSwitchResult.available_backends still exposes obsolete field {obsolete_field!r}"
                    )

        bounds = {
            "max_concurrent_requests": ("minimum: 0", "maximum: 64", "default: 64"),
            "max_queue_size": ("minimum: 0", "maximum: 1024", "default: 0"),
            "queue_wait_timeout_seconds": ("minimum: 0", "maximum: 3600", "default: 30"),
            "requests_per_minute": ("minimum: 0", "maximum: 100000", "default: 0"),
            "token_budget_per_day": ("minimum: 0", "maximum: 1000000000000", "default: 0"),
            "request_timeout_seconds": ("minimum: 0", "maximum: 86400", "default: 0"),
        }
        for field, constraints in bounds.items():
            try:
                block = schema_field_block(mutation, field)
                for constraint in constraints:
                    require(
                        constraint in block,
                        f"ProviderMutation.{field}: missing contract constraint {constraint!r}",
                    )
            except ValueError:
                pass

        create_response = response_block(
            operation_block(source, "/api/admin/providers", "post"), "201"
        )
        require(
            "#/components/schemas/ProviderList" in create_response,
            "POST /api/admin/providers 201 must describe ProviderList",
        )
        create_operation = operation_block(source, "/api/admin/providers", "post")
        duplicate_response = response_block(create_operation, "409")
        require(
            "#/components/responses/AdminError" in duplicate_response,
            "POST /api/admin/providers 409 must describe the duplicate-ID conflict response",
        )
        require(
            "provider_id_exists" in create_operation and "409" in create_operation,
            "Provider creation must document the stable provider_id_exists conflict code",
        )

        # 调用方 API Key 名称冲突必须与创建 Handler 的 409 响应保持一致。
        key_create_operation = operation_block(source, "/api/admin/keys", "post")
        key_create_conflict = response_block(key_create_operation, "409")
        require(
            "#/components/responses/AdminError" in key_create_conflict,
            "POST /api/admin/keys 409 must use the stable AdminError envelope",
        )
        key_create_handler = go_function(
            request_list_handler, "func (s *Server) keysAPI("
        )
        require(
            "http.StatusConflict" in key_create_handler
            and '"key_name_conflict"' in key_create_handler,
            "keysAPI must return HTTP 409 with key_name_conflict for duplicate names",
        )

        # 调用方 API Key 的各项操作逐一对照 Handler 与稳定响应 Schema。
        key_list_operation = operation_block(source, "/api/admin/keys", "get")
        key_list_response = response_block(key_list_operation, "200")
        require(
            "#/components/schemas/APIKeyList" in key_list_response,
            "GET /api/admin/keys 200 must reference APIKeyList",
        )
        require(
            "#/components/responses/AdminError" in response_block(key_list_operation, "500"),
            "GET /api/admin/keys 500 must use AdminError",
        )
        require(
            "http.MethodGet" in key_create_handler
            and 'jsonError(w, 500, "database_error"' in key_create_handler,
            "keysAPI GET must list key metadata and map storage errors to HTTP 500",
        )

        key_created_response = response_block(key_create_operation, "200")
        require(
            "#/components/schemas/APIKeyCreated" in key_created_response,
            "POST /api/admin/keys 200 must reference APIKeyCreated",
        )
        for status in ("400", "401", "409"):
            response_block(key_create_operation, status)
        require(
            "http.MethodPost" in key_create_handler
            and '"key": raw' in key_create_handler
            and '"api_key": k' in key_create_handler,
            "keysAPI POST must return the one-time plaintext key and metadata",
        )

        key_action_handler = go_function(
            request_list_handler, "func (s *Server) keyAction("
        )
        key_update_operation = operation_block(source, "/api/admin/keys/{id}", "put")
        key_update_response = response_block(key_update_operation, "200")
        require(
            "const: true" in key_update_response,
            "PUT /api/admin/keys/{id} 200 must describe success=true",
        )
        for status in ("400", "401", "500"):
            response_block(key_update_operation, status)
        require(
            "http.MethodPut" in key_action_handler
            and '"success": true' in key_action_handler
            and '"update_failed"' in key_action_handler,
            "keyAction PUT must return success=true and use update_failed on storage errors",
        )

        key_delete_operation = operation_block(source, "/api/admin/keys/{id}", "delete")
        key_delete_response = response_block(key_delete_operation, "204")
        require(
            "content:" not in key_delete_response,
            "DELETE /api/admin/keys/{id} 204 must not declare a response body",
        )
        for status in ("401", "500"):
            response_block(key_delete_operation, status)
        require(
            "http.MethodDelete" in key_action_handler
            and "w.WriteHeader(204)" in key_action_handler
            and '"delete_failed"' in key_action_handler,
            "keyAction DELETE must return bodyless HTTP 204 and map storage errors",
        )

        for action in ("disable", "enable"):
            operation = operation_block(source, f"/api/admin/keys/{{id}}/{action}", "post")
            success = response_block(operation, "200")
            require(
                "#/components/schemas/APIKeyStatusResult" in success,
                f"POST /api/admin/keys/{{id}}/{action} 200 must reference APIKeyStatusResult",
            )
            for status in ("401", "500"):
                response_block(operation, status)
        require(
            'parts[4] == "disable"' in key_action_handler
            and 'parts[4] == "enable"' in key_action_handler
            and '"status": st' in key_action_handler
            and '"update_failed"' in key_action_handler,
            "keyAction disable/enable must return the active/disabled status and stable update error",
        )

        key_rotate_operation = operation_block(source, "/api/admin/keys/{id}/rotate", "post")
        key_rotate_response = response_block(key_rotate_operation, "200")
        require(
            "#/components/schemas/APIKeyRotated" in key_rotate_response,
            "POST /api/admin/keys/{id}/rotate 200 must reference APIKeyRotated",
        )
        for status in ("401", "500"):
            response_block(key_rotate_operation, status)
        require(
            'case "rotate":' in key_action_handler
            and '"key": raw' in key_action_handler
            and '"rotate_failed"' in key_action_handler,
            "keyAction rotate must return a one-time plaintext key and stable rotate error",
        )
        delete_response = response_block(
            operation_block(source, "/api/admin/providers/{id}", "delete"), "204"
        )
        require(
            "content:" not in delete_response,
            "DELETE /api/admin/providers/{id} 204 must not declare a response body",
        )
        stats_error = response_block(
            operation_block(source, "/api/requests/stats", "get"), "500"
        )
        require(
            "#/components/responses/AdminError" in stats_error,
            "GET /api/requests/stats 500 must use the stable AdminError envelope",
        )

        create_handler = go_function(
            provider_handler, "func (s *Server) adminProvidersAPI("
        )
        require(
            "http.MethodPost" in create_handler and "http.StatusCreated" in create_handler,
            "adminProvidersAPI must return HTTP 201 for Provider creation",
        )
        require(
            '"create"' in create_handler and '"provider_id_exists"' in provider_handler,
            "Provider creation must use a duplicate-checked create action and return provider_id_exists",
        )
        require(
            "takeOverDeploymentProvider" not in provider_handler
            and '"source_override"' not in view
            and '"deleted"' not in view,
            "Provider management must not expose deployment takeover or persisted lifecycle flags",
        )
        delete_handler = go_function(
            provider_handler, "func (s *Server) adminProviderActionAPI("
        )
        require(
            "http.MethodDelete" in delete_handler and "http.StatusNoContent" in delete_handler,
            "adminProviderActionAPI must return bodyless HTTP 204 for Provider deletion",
        )
        stats_error_handler = go_function(
            stats_handler, "func writeRequestStatsDatabaseError("
        )
        require(
            "http.StatusInternalServerError" in stats_error_handler
            and '"database_error"' in stats_error_handler
            and "查询请求统计失败" in stats_error_handler,
            "request statistics DB errors must use a generic database_error response",
        )
        require(
            "err)" in stats_error_handler and "applog.Printf" in stats_error_handler,
            "request statistics DB diagnostics must remain in server-side logs",
        )

        # Keep the request-list Provider filter synchronized with the actual Handler.
        request_list_operation = operation_block(source, "/api/requests", "get")
        provider_parameter = re.search(
            r"(?ms)^        - name: provider\n          in: query\n.*?^          schema: \{ type: string \}",
            request_list_operation,
        )
        require(
            provider_parameter is not None,
            "GET /api/requests must document the Provider query filter as a string",
        )
        request_list_impl = go_function(
            request_list_handler, "func (s *Server) requestsAPI("
        )
        require(
            'r.URL.Query().Get("provider")' in request_list_impl
            and "queryRequestLogsMerged" in request_list_impl,
            "requestsAPI must pass the Provider query filter into merged request-log lookup",
        )

        # Cancel API must document all stable status classes returned by the Handler.
        cancel_operation = operation_block(
            source, "/api/requests/{id}/cancel", "post"
        )
        for status in ("200", "400", "401", "405", "409"):
            try:
                response_block(cancel_operation, status)
            except ValueError as exc:
                errors.append(f"POST /api/requests/{{id}}/cancel: {exc}")
        cancel_method_error = response_block(cancel_operation, "405")
        require(
            "#/components/responses/AdminError" in cancel_method_error,
            "POST /api/requests/{id}/cancel 405 must use AdminError",
        )
        cancel_handler = go_function(
            request_cancel_handler, "func (s *Server) cancelRequestAPI("
        )
        require(
            "r.Method != http.MethodPost" in cancel_handler
            and "http.StatusMethodNotAllowed" in cancel_handler
            and "http.StatusBadRequest" in cancel_handler
            and "http.StatusConflict" in cancel_handler
            and '"cancelled": true' in cancel_handler,
            "cancelRequestAPI status/body behavior must match the OpenAPI contract",
        )

        # Operational status endpoints share explicit schemas with their Go DTOs.
        audit_operation = operation_block(source, "/api/admin/audit/metrics", "get")
        audit_response = response_block(audit_operation, "200")
        require(
            "#/components/schemas/AuditMetricsEnvelope" in audit_response,
            "GET /api/admin/audit/metrics 200 must reference AuditMetricsEnvelope",
        )
        audit_envelope = schema_block(source, "AuditMetricsEnvelope")
        require(
            re.search(
                r'(?m)^        data:\n          \$ref:[ \t]*"#/components/schemas/AuditMetrics"',
                audit_envelope,
            ) is not None,
            "AuditMetricsEnvelope.data must reference AuditMetrics",
        )
        audit_schema = schema_block(source, "AuditMetrics")
        audit_decl = re.search(
            r"(?ms)^type StatsSnapshot struct \{(.*?)^\}",
            audit_metrics_source,
        )
        if audit_decl is None:
            raise ValueError("internal/audit/metrics.go: type StatsSnapshot struct not found")
        for field in sorted({tag for tag in re.findall(r'json:"([^",]+)', audit_decl.group(1)) if tag and tag != "-"}):
            try:
                schema_field_block(audit_schema, field)
            except ValueError:
                errors.append(f"AuditMetrics: missing StatsSnapshot JSON field {field!r}")

        retention_operation = operation_block(source, "/api/admin/retention/status", "get")
        retention_response = response_block(retention_operation, "200")
        require(
            re.search(
                r'(?m)^                  data:\n                    \$ref:[ \t]*"#/components/schemas/RequestRetentionStatus"',
                retention_response,
            ) is not None,
            "GET /api/admin/retention/status 200 data must reference RequestRetentionStatus",
        )
        retention_schema = schema_block(source, "RequestRetentionStatus")
        runtime_field = schema_field_block(retention_schema, "runtime")
        require(
            re.search(
                r'(?m)^        runtime:\n          \$ref:[ \t]*"#/components/schemas/RequestRetentionRuntimeStatus"',
                runtime_field,
            ) is not None,
            "RequestRetentionStatus.runtime must reference RequestRetentionRuntimeStatus",
        )
        retention_decl = re.search(
            r"(?ms)^type requestRetentionStatusResponse struct \{(.*?)^\}",
            request_retention_source,
        )
        if retention_decl is None:
            raise ValueError("internal/server/request_retention.go: type requestRetentionStatusResponse not found")
        for field in sorted({tag for tag in re.findall(r'json:"([^",]+)', retention_decl.group(1)) if tag and tag != "-"}):
            try:
                schema_field_block(retention_schema, field)
            except ValueError:
                errors.append(f"RequestRetentionStatus: missing response JSON field {field!r}")

        retention_runtime_schema = schema_block(source, "RequestRetentionRuntimeStatus")
        retention_runtime_decl = re.search(
            r"(?ms)^type requestRetentionRuntimeStatus struct \{(.*?)^\}",
            request_retention_source,
        )
        if retention_runtime_decl is None:
            raise ValueError("internal/server/request_retention.go: type requestRetentionRuntimeStatus not found")
        for field in sorted({tag for tag in re.findall(r'json:"([^",]+)', retention_runtime_decl.group(1)) if tag and tag != "-"}):
            try:
                schema_field_block(retention_runtime_schema, field)
            except ValueError:
                errors.append(f"RequestRetentionRuntimeStatus: missing runtime JSON field {field!r}")

        # RequestDetail nested analysis/stream DTOs must match their Go structs.
        request_detail_schema = schema_block(source, "RequestDetail")
        require(
            re.search(
                r'(?m)^        analysis:\n          \$ref:[ \t]*"#/components/schemas/RequestAnalysis"',
                request_detail_schema,
            ) is not None,
            "RequestDetail.analysis must reference RequestAnalysis",
        )
        require(
            re.search(
                r'(?ms)^        stream_events:\n          type: array\n          items:\n            \$ref:[ \t]*"#/components/schemas/StreamEvent"',
                request_detail_schema,
            ) is not None,
            "RequestDetail.stream_events.items must reference StreamEvent",
        )
        for type_name in ("RequestAnalysis", "RequestPhaseMS", "RequestThroughput"):
            schema = schema_block(source, type_name)
            pattern = r"(?ms)^type " + re.escape(type_name) + r" struct \{(.*?)^\}"
            declaration = re.search(pattern, request_analysis_source)
            if declaration is None:
                raise ValueError(f"internal/server/request_analysis.go: type {type_name} struct not found")
            fields = {
                tag
                for tag in re.findall(r'json:"([^",]+)', declaration.group(1))
                if tag and tag != "-"
            }
            for field in sorted(fields):
                try:
                    schema_field_block(schema, field)
                except ValueError:
                    errors.append(f"{type_name}: missing Go JSON field {field!r}")

        analysis_schema = schema_block(source, "RequestAnalysis")
        require(
            re.search(
                r'(?m)^        phases_ms:\n          \$ref:[ \t]*"#/components/schemas/RequestPhaseMS"',
                analysis_schema,
            ) is not None,
            "RequestAnalysis.phases_ms must reference RequestPhaseMS",
        )
        require(
            re.search(
                r'(?m)^        throughput:\n          \$ref:[ \t]*"#/components/schemas/RequestThroughput"',
                analysis_schema,
            ) is not None,
            "RequestAnalysis.throughput must reference RequestThroughput",
        )

        stream_event_schema = schema_block(source, "StreamEvent")
        stream_event_decl = re.search(
            r"(?ms)^type StreamEvent struct \{(.*?)^\}",
            store_request_log,
        )
        if stream_event_decl is None:
            raise ValueError("internal/store/store.go: type StreamEvent struct not found")
        stream_event_fields = {
            tag
            for tag in re.findall(r'json:"([^",]+)', stream_event_decl.group(1))
            if tag and tag != "-"
        }
        for field in sorted(stream_event_fields):
            try:
                schema_field_block(stream_event_schema, field)
            except ValueError:
                errors.append(f"StreamEvent: missing Go JSON field {field!r}")

        # RequestList and RequestDetail.log expose store.RequestLog JSON.
        request_log_schema = schema_block(source, "RequestLog")
        request_list_schema = schema_block(source, "RequestList")
        require(
            re.search(
                r'(?m)^\s+items:[ \t]*\n\s+\$ref:[ \t]*"#/components/schemas/RequestLog"',
                request_list_schema,
            ) is not None,
            "RequestList.data.items must reference RequestLog",
        )
        request_detail_schema = schema_block(source, "RequestDetail")
        require(
            re.search(
                r'(?m)^        log:\n          \$ref:[ \t]*"#/components/schemas/RequestLog"',
                request_detail_schema,
            ) is not None,
            "RequestDetail.log must reference RequestLog",
        )
        request_log_decl = re.search(
            r"(?ms)^type RequestLog struct \{(.*?)^\}",
            store_request_log,
        )
        if request_log_decl is None:
            raise ValueError("internal/store/store.go: type RequestLog struct not found")
        request_log_fields = {
            tag
            for tag in re.findall(r'json:"([^",]+)', request_log_decl.group(1))
            if tag and tag != "-"
        }
        for field in sorted(request_log_fields):
            try:
                schema_field_block(request_log_schema, field)
            except ValueError:
                errors.append(f"RequestLog: missing Go JSON field {field!r}")

        # Keep every stable top-level response key returned by the detail builder/handler in OpenAPI.
        detail_keys = {"log", "summary", "timing", "diagnosis"}
        detail_keys.update(re.findall(r'out\["([a-z][a-z0-9_]*)"\]', request_detail_source))
        detail_handler = go_function(request_list_handler, "func (s *Server) requestDetailAPI(")
        detail_keys.update(re.findall(r'out\["([a-z][a-z0-9_]*)"\]', detail_handler))
        require(
            "additionalProperties: false" in request_detail_schema,
            "RequestDetail must reject undocumented top-level properties",
        )
        for field in sorted(detail_keys):
            try:
                schema_field_block(request_detail_schema, field)
            except ValueError:
                errors.append(f"RequestDetail: missing handler/helper field {field!r}")
        # RequestDetail summary/timing are dynamic maps; keep their key sets aligned with helpers.
        summary_schema = schema_block(source, "RequestSummary")
        timing_schema = schema_block(source, "RequestTiming")
        detail_schema = schema_block(source, "RequestDetail")
        require(
            re.search(
                r'(?m)^        summary:\n          \$ref:[ \t]*"#/components/schemas/RequestSummary"',
                detail_schema,
            ) is not None,
            "RequestDetail.summary must reference RequestSummary",
        )
        require(
            re.search(
                r'(?m)^        timing:\n          \$ref:[ \t]*"#/components/schemas/RequestTiming"',
                detail_schema,
            ) is not None,
            "RequestDetail.timing must reference RequestTiming",
        )
        summary_fn = go_function(request_detail_source, "func buildRequestSummary(")
        summary_keys = set(re.findall(r'"([a-z][a-z0-9_]*)"\s*:', summary_fn))
        summary_keys.update(re.findall(r's\["([a-z][a-z0-9_]*)"\]', summary_fn))
        summary_keys.update(re.findall(r'summary\["([a-z][a-z0-9_]*)"\]', go_function(request_detail_source, "func buildRequestDetail(")))
        for field in sorted(summary_keys):
            try:
                schema_field_block(summary_schema, field)
            except ValueError:
                errors.append(f"RequestSummary: missing helper field {field!r}")

        timing_fn = go_function(request_detail_source, "func buildRequestTiming(")
        timing_keys = set(re.findall(r'"([a-z][a-z0-9_]*)"\s*:', timing_fn))
        timing_keys.update(re.findall(r't\["([a-z][a-z0-9_]*)"\]', timing_fn))
        timing_keys.update(re.findall(r'timing\["([a-z][a-z0-9_]*)"\]', go_function(request_detail_source, "func buildRequestDetail(")))
        timing_keys.update(re.findall(r'timing\["([a-z][a-z0-9_]*)"\]', go_function(request_detail_source, "func applyHarvestToTiming(")))
        for field in sorted(timing_keys):
            try:
                schema_field_block(timing_schema, field)
            except ValueError:
                errors.append(f"RequestTiming: missing helper field {field!r}")
        # Public auth status must describe exactly the stable, non-secret fields emitted by the Handler.
        auth_status_operation = operation_block(source, "/api/auth/status", "get")
        auth_status_response = response_block(auth_status_operation, "200")
        require(
            '"#/components/schemas/AuthStatus"' in auth_status_response,
            "GET /api/auth/status 200 must reference AuthStatus",
        )
        auth_status_schema = schema_block(source, "AuthStatus")
        auth_status_handler = go_function(auth_handler, "func (s *Server) authStatusAPI(")
        require(
            '"password_enabled": snap.PasswordEnabled' in auth_status_handler,
            "authStatusAPI must expose password_enabled from the current login snapshot",
        )
        require(
            '"password_source":  snap.PasswordSource' in auth_status_handler,
            "authStatusAPI must expose password_source from the current login snapshot",
        )
        require(
            'w.Header().Set("Cache-Control", "no-store")' in auth_status_handler,
            "authStatusAPI must prevent caching of current credential-source state",
        )
        for field, kind in (("password_enabled", "boolean"), ("password_source", "string")):
            try:
                field_schema = schema_field_block(auth_status_schema, field)
                require(
                    f"type: {kind}" in field_schema,
                    f"AuthStatus.{field} must declare type {kind}",
                )
            except ValueError as exc:
                errors.append(f"AuthStatus: {exc}")
        try:
            source_field = schema_field_block(auth_status_schema, "password_source")
            require(
                "enum: [database, deployment]" in source_field,
                "AuthStatus.password_source must enumerate database and deployment",
            )
        except ValueError as exc:
            errors.append(f"AuthStatus: {exc}")

        # Credential mutation response includes the updated password_enabled state;
        # it must not be documented as SessionUser alone.
        credentials_put = operation_block(source, "/api/auth/credentials", "put")
        credentials_response = response_block(credentials_put, "200")
        require(
            '"#/components/schemas/CredentialsMutationResponse"' in credentials_response,
            "PUT /api/auth/credentials 200 must reference CredentialsMutationResponse",
        )
        credentials_response_schema = schema_block(source, "CredentialsMutationResponse")
        credentials_put_handler = go_function(auth_handler, "func (s *Server) authCredentialsPut(")
        for field in ("user", "password_enabled"):
            require(
                f'"{field}":' in credentials_put_handler,
                f"authCredentialsPut must return {field}",
            )
            try:
                schema_field_block(credentials_response_schema, field)
            except ValueError as exc:
                errors.append(f"CredentialsMutationResponse: {exc}")
        require(
            '"username": webAdminIdentity' in credentials_put_handler,
            "authCredentialsPut must include the updated user username field",
        )
        try:
            user_field = schema_field_block(credentials_response_schema, "user")
            require("username:" in user_field, "CredentialsMutationResponse.user must declare username")
        except ValueError as exc:
            errors.append(f"CredentialsMutationResponse: {exc}")

        # 下载任务列表字段必须与 downloadsAPI 的实际 JSON DTO 保持一致。
        downloads_handler = go_function(request_list_handler, "func (s *Server) downloadsAPI(")
        download_item = re.search(r"(?ms)^\s*type item struct \{(.*?)^\s*\}", downloads_handler)
        if download_item is None:
            raise ValueError("downloadsAPI item response struct not found")
        download_json_fields = set(re.findall(r'json:"([^",]+)', download_item.group(1)))
        downloads_schema = schema_block(source, "DownloadsEnvelope")
        download_item_schema = re.search(
            r"(?ms)^        data:\n          type: array\n          items:\n(.*?)(?=^      additionalProperties:)",
            downloads_schema,
        )
        if download_item_schema is None:
            raise ValueError("DownloadsEnvelope.data.items schema not found")
        download_item_fields = set(
            re.findall(r"(?m)^              ([a-z][a-z0-9_]*):", download_item_schema.group(1))
        )
        for field in sorted(download_json_fields):
            if field not in download_item_fields:
                errors.append(f"DownloadsEnvelope item: missing Handler JSON field {field!r}")
        required_line = next(
            (line for line in download_item_schema.group(1).splitlines() if line.strip().startswith("required: [")),
            "",
        )
        required_fields = (
            set(required_line.partition("[")[2].partition("]")[0].replace(" ", "").split(","))
            if required_line
            else set()
        )
        for field in ("model_key", "kind", "status", "done", "total"):
            require(
                field in required_fields,
                f"DownloadsEnvelope item must require {field}",
            )
        kind_field = re.search(r"(?m)^              kind:.*$", download_item_schema.group(1))
        status_field = re.search(r"(?m)^              status:.*$", download_item_schema.group(1))
        require(
            kind_field is not None and "enum: [hf, ollama]" in kind_field.group(0),
            "DownloadsEnvelope.kind must enumerate the Handler's hf/ollama task kinds",
        )
        require(
            status_field is not None and "enum: [downloading, download_failed, download_cancelled, ready]" in status_field.group(0),
            "DownloadsEnvelope.status must enumerate the supported download states",
        )
        require(
            "additionalProperties: false" in download_item_schema.group(1),
            "DownloadsEnvelope item must reject undocumented response fields",
        )
        require(
            "additionalProperties: false" in downloads_schema,
            "DownloadsEnvelope must reject undocumented top-level response fields",
        )

        dismiss_handler = go_function(download_dismiss_handler, "func (s *Server) downloadsDismissAPI(")
        require(
            "ClearFailedDownloads(ctx)" in dismiss_handler and "DismissDownloadRecord(ctx, key)" in dismiss_handler,
            "downloadsDismissAPI must preserve single-item and all-failed cleanup paths",
        )
        dismiss_request = schema_block(source, "DownloadDismissRequest")
        require(
            "anyOf:" in dismiss_request and "all_failed: { type: boolean, const: true }" in dismiss_request,
            "DownloadDismissRequest must require model_key or all_failed=true",
        )
        dismiss_operation = operation_block(source, "/api/downloads/dismiss", "post")
        dismiss_response = response_block(dismiss_operation, "200")
        require(
            '"#/components/schemas/DownloadDismissResult"' in dismiss_response,
            "POST /api/downloads/dismiss 200 must reference DownloadDismissResult",
        )

        # Ollama 专用管理 API 必须逐条登记，不能只依赖宽泛的 RuntimeSnapshot。
        ollama_routes = {
            "/api/admin/ollama": ("get", "ollamaAdminAPI"),
            "/api/admin/ollama/pull": ("post", "ollamaPullAPI"),
            "/api/admin/ollama/pull/cancel": ("post", "ollamaPullCancelAPI"),
            "/api/admin/ollama/load": ("post", "ollamaLoadAPI"),
            "/api/admin/ollama/unload": ("post", "ollamaUnloadAPI"),
            "/api/admin/ollama/delete": ("post", "ollamaDeleteAPI"),
            "/api/admin/catalog/ollama/tags": ("get", "ollamaCatalogTagsAPI"),
        }
        for route, (method, handler_name) in ollama_routes.items():
            route_block = operation_block(source, route, method)
            require(
                "AdminSessionBearer: []" in route_block,
                f"{method.upper()} {route} must require an admin web session",
            )
            require(
                f'HandleFunc("{route}", s.{handler_name})' in request_list_handler,
                f"{route} must remain registered to {handler_name}",
            )
        ollama_list_handler = go_function(ollama_handler, "func (s *Server) ollamaAdminAPI(")
        for field in ("base_url", "catalog", "loaded", "active"):
            require(
                f'"{field}"' in ollama_list_handler,
                f"ollamaAdminAPI response must include {field}",
            )
        catalog_decl = re.search(r"(?ms)^type CatalogItem struct \{(.*?)^\}", runtime_types)
        if catalog_decl is None:
            raise ValueError("internal/llama/runtime.go: CatalogItem struct not found")
        catalog_schema = schema_block(source, "OllamaCatalogItem")
        for field in sorted({tag for tag in re.findall(r'json:"([^",]+)', catalog_decl.group(1)) if tag and tag != "-" }):
            try:
                schema_field_block(catalog_schema, field)
            except ValueError:
                errors.append(f"OllamaCatalogItem: missing CatalogItem JSON field {field!r}")
        for handler_name, request_fields, response_fields in (
            ("ollamaPullAPI", ("name",), ("success", "name", "job_id")),
            ("ollamaPullCancelAPI", ("job_id",), ("success",)),
            ("ollamaLoadAPI", ("name",), ("success", "name")),
            ("ollamaUnloadAPI", ("name",), ("success", "name")),
            ("ollamaDeleteAPI", ("name",), ("success", "logs")),
        ):
            handler = go_function(ollama_handler, f"func (s *Server) {handler_name}(")
            for field in request_fields:
                require(
                    f'json:"{field}"' in handler,
                    f"{handler_name} request must decode {field}",
                )
            for field in response_fields:
                require(
                    f'"{field}"' in handler or (handler_name == "ollamaDeleteAPI" and field in ("success", "logs") and "writeModelOpsResult" in handler),
                    f"{handler_name} response must expose {field}",
                )
        tags_handler = go_function(catalog_handler, "func (s *Server) ollamaCatalogTagsAPI(")
        require(
            'r.URL.Query().Get("model")' in tags_handler
            and '"model"' in tags_handler
            and '"tags"' in tags_handler,
            "ollamaCatalogTagsAPI must accept model query and return model/tags",
        )
        tag_decl = re.search(r"(?ms)^type Tag struct \{(.*?)^\}", ollama_tag_types)
        if tag_decl is None:
            raise ValueError("internal/catalog/ollama/tags.go: Tag struct not found")
        tag_schema = schema_block(source, "OllamaTagList")
        tag_items = re.search(
            r"(?ms)^        tags:\n          type: array\n          items:\n            type: object\n(.*?)(?=^      additionalProperties:)",
            tag_schema,
        )
        if tag_items is None:
            raise ValueError("OllamaTagList.tags.items schema not found")
        tag_fields = set(re.findall(r"(?m)^              ([a-z][a-z0-9_]*):", tag_items.group(1)))
        for field in sorted({tag for tag in re.findall(r'json:"([^",]+)', tag_decl.group(1)) if tag and tag != "-"}):
            require(
                field in tag_fields,
                f"OllamaTagList.tags: missing Tag JSON field {field!r}",
            )

        # 统一运行时 API：请求至少指定 model_id/model_key，卸载结果与 Go DTO 对齐。
        runtime_routes = {
            "/api/admin/runtime": ("get", "runtimeAPI"),
            "/api/admin/runtime/preview": ("get", "runtimePreviewAPI"),
            "/api/admin/runtime/refresh": ("post", "runtimeRefreshAPI"),
            "/api/admin/runtime/select": ("post", "runtimeSelectAPI"),
            "/api/admin/runtime/unload": ("post", "runtimeUnloadAPI"),
        }
        for route, (method, handler_name) in runtime_routes.items():
            operation = operation_block(source, route, method)
            require(
                "AdminSessionBearer: []" in operation,
                f"{method.upper()} {route} must require an admin web session",
            )
            require(
                f'HandleFunc("{route}", s.{handler_name})' in request_list_handler,
                f"{route} must remain registered to {handler_name}",
            )
            require(
                '"405":' in response_block(operation, "405"),
                f"{method.upper()} {route} must document Handler's 405 response",
            )
        runtime_request = schema_block(source, "RuntimeModelRequest")
        require(
            "anyOf:" in runtime_request
            and "required: [model_id]" in runtime_request
            and "required: [model_key]" in runtime_request,
            "RuntimeModelRequest must require model_id or model_key",
        )
        runtime_unload = go_function(runtime_handler, "func (s *Server) runtimeUnloadAPI(")
        for field in ("model_id", "model_key"):
            require(
                f'json:"{field}"' in runtime_unload,
                f"runtimeUnloadAPI request must decode {field}",
            )
        unload_schema = schema_block(source, "RuntimeUnloadResult")
        unload_decl = re.search(r"(?ms)^type InferenceUnloadResult struct \\{(.*?)^\\}", inference_ops)
        if unload_decl is None:
            raise ValueError("internal/server/inference_ops.go: InferenceUnloadResult struct not found")
        unload_fields = {tag for tag in re.findall(r'json:"([^",]+)', unload_decl.group(1)) if tag and tag != "-"}
        for field in sorted(unload_fields):
            try:
                schema_field_block(unload_schema, field)
            except ValueError:
                errors.append(f"RuntimeUnloadResult: missing InferenceUnloadResult JSON field {field!r}")
        require(
            '"success"' in go_function(inference_ops, "func (r InferenceUnloadResult) toJSON()"),
            "InferenceUnloadResult.toJSON must keep its synthetic success field",
        )
        unload_response = response_block(operation_block(source, "/api/admin/runtime/unload", "post"), "200")
        require(
            '"#/components/schemas/RuntimeUnloadResult"' in unload_response,
            "POST /api/admin/runtime/unload 200 must reference RuntimeUnloadResult",
        )
        select_response = response_block(operation_block(source, "/api/admin/runtime/select", "post"), "200")
        require(
            '"#/components/schemas/RuntimeActionResult"' in select_response,
            "POST /api/admin/runtime/select 200 must reference RuntimeActionResult",
        )
        runtime_schema = schema_block(source, "RuntimeSnapshot")
        for field in ("backend", "active_model", "loaded", "catalog", "remote_provider", "hot_unload_supported"):
            try:
                schema_field_block(runtime_schema, field)
            except ValueError:
                errors.append(f"RuntimeSnapshot: missing runtime field {field!r}")

        # GET /api/models returns GatewayModel rows, including runtime-name
        # fields synthesized by ListGatewayModelRows rather than model.Model.
        model_schema = schema_block(source, "GatewayModel")
        model_response = response_block(
            operation_block(source, "/api/models", "get"), "200"
        )
        require(
            re.search(
                r'(?m)^\s+items:[ \t]*\n\s+\$ref:[ \t]*"#/components/schemas/GatewayModel"',
                model_response,
            ) is not None,
            "GET /api/models 200 data.items must reference GatewayModel",
        )
        model_decl = re.search(
            r"(?ms)^type Model struct \{(.*?)^\}",
            model_struct,
        )
        if model_decl is None:
            raise ValueError("internal/model/model.go: type Model struct not found")
        json_fields = {
            tag
            for tag in re.findall(r'json:"([^",]+)', model_decl.group(1))
            if tag and tag != "-"
        }
        for field in sorted(json_fields):
            try:
                schema_field_block(model_schema, field)
            except ValueError:
                errors.append(f"GatewayModel: missing Go Model JSON field {field!r}")

        for field in (
            "upstream_llamacpp_id",
            "upstream_ollama_name",
            "upstream_name",
            "disk_bytes",
        ):
            if re.search(rf'(?m)row\["{re.escape(field)}"\]', gateway_model_ops) is not None:
                try:
                    schema_field_block(model_schema, field)
                except ValueError as exc:
                    errors.append(f"GatewayModel: missing synthesized field {field!r}: {exc}")
    except ValueError as exc:
        errors.append(str(exc))

    return errors


def main() -> int:
    try:
        source = OPENAPI_FILE.read_text(encoding="utf-8")
        provider_handler = PROVIDER_HANDLER_FILE.read_text(encoding="utf-8")
        stats_handler = REQUEST_STATS_FILE.read_text(encoding="utf-8")
        model_struct = MODEL_STRUCT_FILE.read_text(encoding="utf-8")
        gateway_model_ops = GATEWAY_MODEL_OPS_FILE.read_text(encoding="utf-8")
        request_list_handler = REQUEST_LIST_HANDLER_FILE.read_text(encoding="utf-8")
        request_cancel_handler = REQUEST_CANCEL_HANDLER_FILE.read_text(encoding="utf-8")
        store_request_log = STORE_REQUEST_LOG_FILE.read_text(encoding="utf-8")
        request_detail_source = REQUEST_DETAIL_SOURCE_FILE.read_text(encoding="utf-8")
        request_analysis_source = REQUEST_ANALYSIS_FILE.read_text(encoding="utf-8")
        audit_metrics_source = AUDIT_METRICS_FILE.read_text(encoding="utf-8")
        request_retention_source = REQUEST_RETENTION_FILE.read_text(encoding="utf-8")
        auth_handler = AUTH_HANDLER_FILE.read_text(encoding="utf-8")
        download_dismiss_handler = DOWNLOAD_DISMISS_HANDLER_FILE.read_text(encoding="utf-8")
        ollama_handler = OLLAMA_HANDLER_FILE.read_text(encoding="utf-8")
        catalog_handler = CATALOG_HANDLER_FILE.read_text(encoding="utf-8")
        runtime_types = RUNTIME_TYPES_FILE.read_text(encoding="utf-8")
        ollama_tag_types = OLLAMA_TAG_TYPES_FILE.read_text(encoding="utf-8")
        runtime_handler = REQUEST_LIST_HANDLER_FILE.read_text(encoding="utf-8")
        inference_ops = INFERENCE_OPS_FILE.read_text(encoding="utf-8")
        errors = validate(
            source,
            provider_handler,
            stats_handler,
            model_struct,
            gateway_model_ops,
            request_list_handler,
            request_cancel_handler,
            store_request_log,
            request_detail_source,
            request_analysis_source,
            audit_metrics_source,
            request_retention_source,
            auth_handler,
            download_dismiss_handler,
            ollama_handler,
            catalog_handler,
            runtime_types,
            ollama_tag_types,
            inference_ops,
        )
        errors.extend(admin_api_key_scope_errors(source, auth_handler))
    except OSError as exc:
        print(f"OpenAPI high-risk contract check could not run: {exc}", file=sys.stderr)
        return 2

    if errors:
        print("OpenAPI high-risk contract check failed:", file=sys.stderr)
        for error in errors:
            print(f"  - {error}", file=sys.stderr)
        return 1

    print(
        "OpenAPI high-risk contracts OK: Provider mutation/view fields and bounds, "
        "Provider create/delete status/body, request-stats DB error envelope, "
        "request-list Provider filtering, request-cancel status contract, Admin API Key scope, "
        "/api/models GatewayModel fields, RequestLog fields, RequestDetail fields, RequestAnalysis/StreamEvent fields, AuditMetrics fields, and request retention status fields, public auth status and credential mutation response fields."
    )
    print(
        "Note: this is a focused gate; full field-level Go/OpenAPI schema equivalence "
        "remains partial."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
