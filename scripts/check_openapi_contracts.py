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
REQUEST_LIST_HANDLER_FILE = ROOT / "internal" / "server" / "server.go"
REQUEST_CANCEL_HANDLER_FILE = ROOT / "internal" / "server" / "pipeline_http.go"


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
        "/api/models GatewayModel fields, RequestLog fields, and RequestDetail summary/timing fields."
    )
    print(
        "Note: this is a focused gate; full field-level Go/OpenAPI schema equivalence "
        "remains partial."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
