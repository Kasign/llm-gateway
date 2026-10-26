#!/usr/bin/env python3
"""Compare registered Gateway HTTP route paths with the OpenAPI path keys.

This intentionally checks path coverage only. It does not claim to validate
per-operation request/response schemas against every Handler implementation.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ROUTER_FILE = ROOT / "internal" / "server" / "server.go"
OPENAPI_FILE = ROOT / "docs" / "openapi.yaml"

# These handlers use Go ServeMux prefixes or path parsing rather than a literal
# mux.HandleFunc pattern. Keep their route families explicit and reviewable.
ROUTE_FAMILIES = {
    "/api/models/": (
        "/api/models/{model_key}",
        "/api/models/{model_key}/{action}",
    ),
    "/api/requests/": (
        "/api/requests/{id}",
        "/api/requests/{id}/cancel",
    ),
    "/api/admin/providers/": (
        "/api/admin/providers/{id}",
        "/api/admin/providers/{id}/enable",
        "/api/admin/providers/{id}/disable",
        "/api/admin/providers/{id}/clear-key",
        "/api/admin/providers/{id}/test",
    ),
    "/api/benchmark/": (
        "/api/benchmark/{job_id}",
    ),
    "/api/admin/keys/": (
        "/api/admin/keys/{id}",
        "/api/admin/keys/{id}/disable",
        "/api/admin/keys/{id}/enable",
        "/api/admin/keys/{id}/rotate",
    ),
}

# /v1/ and {agentPrefix}/v1/ dispatch to the shared inference router. Keep the
# publicly documented, supported protocols explicit rather than claiming that
# every arbitrary suffix is a stable public contract.
INFERENCE_PATHS = (
    "/v1/models",
    "/v1/chat/completions",
    "/v1/completions",
    "/v1/responses",
    "/v1/messages",
    "/v1/messages/count_tokens",
)
AGENT_PREFIX_PATHS = tuple(
    "/{agentPrefix}" + path for path in INFERENCE_PATHS
)
NON_OPENAPI_ROUTES = {"/"}  # The root is the HTML console, not an API operation.


def read_openapi_paths() -> set[str]:
    text = OPENAPI_FILE.read_text(encoding="utf-8")
    if "\npaths:\n" not in text or "\ncomponents:\n" not in text:
        raise ValueError("unexpected OpenAPI document layout")
    paths_section = text.split("\npaths:\n", 1)[1].split("\ncomponents:\n", 1)[0]
    return set(re.findall(r"^  (/[^:\n]+):\s*$", paths_section, re.MULTILINE))


def registered_api_paths() -> set[str]:
    source = ROUTER_FILE.read_text(encoding="utf-8")
    routes = set(re.findall(r'mux\.HandleFunc\("([^"]+)"', source))
    expected: set[str] = set()

    for route in sorted(routes):
        if route in NON_OPENAPI_ROUTES or route == "/static/":
            continue
        if route == "/v1/":
            expected.update(INFERENCE_PATHS)
        elif route in ROUTE_FAMILIES:
            expected.update(ROUTE_FAMILIES[route])
        elif route.endswith("/"):
            raise ValueError(
                f"unmapped dynamic route prefix {route!r}; add an explicit "
                "ROUTE_FAMILIES or prefix mapping before updating the OpenAPI contract"
            )
        else:
            expected.add(route)

    if 'mux.Handle(route.PathPrefix+"/v1/models"' not in source:
        raise ValueError("Agent model-list route registration was not found")
    if 'mux.Handle(route.PathPrefix+"/v1/"' not in source:
        raise ValueError("Agent inference route registration was not found")
    expected.update(AGENT_PREFIX_PATHS)
    return expected


def main() -> int:
    try:
        registered = registered_api_paths()
        documented = read_openapi_paths()
    except (OSError, ValueError) as exc:
        print(f"OpenAPI route coverage check could not run: {exc}", file=sys.stderr)
        return 2

    missing = sorted(registered - documented)
    stale = sorted(documented - registered)
    if missing or stale:
        if missing:
            print("Registered Gateway routes missing from docs/openapi.yaml:", file=sys.stderr)
            for path in missing:
                print(f"  + {path}", file=sys.stderr)
        if stale:
            print("OpenAPI paths without a matching registered route family:", file=sys.stderr)
            for path in stale:
                print(f"  - {path}", file=sys.stderr)
        print(
            "Update the route contract and its explicit dynamic-prefix mappings; "
            "this check intentionally does not compare every JSON schema field.",
            file=sys.stderr,
        )
        return 1

    print(
        "OpenAPI path coverage OK: "
        f"{len(registered)} registered API route paths match {len(documented)} OpenAPI paths."
    )
    print(
        "Note: this check covers path registration only, not full Handler/schema equivalence."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
