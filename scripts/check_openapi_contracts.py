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
        )
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
        "request-list Provider filtering, request-cancel status contract, "
        "/api/models GatewayModel fields, RequestLog fields, RequestDetail fields, RequestAnalysis/StreamEvent fields, AuditMetrics fields, and request retention status fields, public auth status and credential mutation response fields."
    )
    print(
        "Note: this is a focused gate; full field-level Go/OpenAPI schema equivalence "
        "remains partial."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
