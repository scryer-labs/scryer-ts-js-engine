"""Exercise the packaged v1 ABI with a temporary source project."""
import ctypes
import json
import sys
import tempfile
from pathlib import Path

package = Path(sys.argv[1]).resolve()
manifest = json.loads((package / "engine.json").read_text())
library = ctypes.CDLL(str(package / manifest["library"]))


class View(ctypes.Structure):
    _fields_ = [("data", ctypes.c_char_p), ("size", ctypes.c_size_t)]


class Buffer(ctypes.Structure):
    _fields_ = [("data", ctypes.c_void_p), ("size", ctypes.c_size_t)]


class Result(ctypes.Structure):
    _fields_ = [("status", ctypes.c_int32), ("response", Buffer)]


library.execute_v1.argtypes = [View]
library.execute_v1.restype = Result
library.cancel_v1.argtypes = [View]
library.cancel_v1.restype = ctypes.c_bool
library.release_buffer_v1.argtypes = [Buffer]
library.release_buffer_v1.restype = None
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory).resolve()
    (root / "main.ts").write_text("export function run() {}\n")
    request = json.dumps({
        "protocolVersion": 1, "requestId": "package-check", "stack": "ts-js",
        "operation": "scan", "scan": {"repository": str(root), "facts": ["files"], "static": True},
    }).encode()
    result = library.execute_v1(View(request, len(request)))
    try:
        response = json.loads(ctypes.string_at(result.response.data, result.response.size))
        assert result.status == 0 and response["error"] is None, response
        assert any(item["path"] == "main.ts" for item in response["report"]["facts"]["files"]["files"])
    finally:
        library.release_buffer_v1(result.response)


# Shared analysis operations must also survive packaging and ABI transport.
assert {"scan", "impact", "coverage"} <= set(manifest["capabilities"]["operations"])
def execute(request):
    data = json.dumps({"protocolVersion": 1, "requestId": "analysis-package-"+request["operation"], "stack": 'ts-js', **request}).encode()
    result = library.execute_v1(View(data, len(data)))
    try:
        response = json.loads(ctypes.string_at(result.response.data, result.response.size))
        assert result.status == 0 and response["error"] is None, response
        return response["report"]
    finally:
        library.release_buffer_v1(result.response)

with tempfile.TemporaryDirectory() as directory:
    root = Path(directory).resolve()
    before, after = root / "before", root / "after"
    before.mkdir(); after.mkdir()
    (before / "value.js").write_text('export function value() { return 1; }\n')
    (after / "value.js").write_text('export function value() { return 2; }\n')
    report = execute({"operation":"impact", "impact":{"before":"a"*40, "after":"b"*40, "beforeRoot":str(before), "afterRoot":str(after), "resolve":False}})
    left, right = report["before"]["symbols"][0], report["after"]["symbols"][0]
    assert left["key"] == right["key"] and left["fingerprint"] != right["fingerprint"], report

    # Anonymous body projection must survive the native DLL/dylib ABI, source
    # offsets, CRLF and a non-ASCII path, not merely an in-process unit test.
    for checkout, value in [(before, 1), (after, 2)]:
        folder = checkout / "callback space 世界"
        folder.mkdir()
        (folder / "callback.js").write_bytes(
            f"export function outer() {{ [1].map(() => {value}); }}\r\n".encode("utf-8"))
    callbacks = execute({"operation":"impact", "impact":{"before":"a"*40, "after":"b"*40, "beforeRoot":str(before), "afterRoot":str(after), "resolve":False}})
    left_callbacks = [s for s in callbacks["before"]["symbols"] if "callback " in s["signature"]]
    right_callbacks = [s for s in callbacks["after"]["symbols"] if "callback " in s["signature"]]
    assert len(left_callbacks) == len(right_callbacks) == 1, callbacks
    assert left_callbacks[0]["key"] == right_callbacks[0]["key"], callbacks
    assert left_callbacks[0]["fingerprint"] != right_callbacks[0]["fingerprint"], callbacks
    assert left_callbacks[0]["role"] != "TEST", callbacks
    source = root / "value.js"
    source.write_text('export function value() { return 1; }\n')
    artifact = root / "coverage.json"
    artifact.write_text(json.dumps({str(source):{"path":str(source),"statementMap":{"0":{"start":{"line":1,"column":26},"end":{"line":1,"column":34}}},"s":{"0":1},"fnMap":{"0":{}},"f":{"0":1},"branchMap":{},"b":{}}}))
    coverage = execute({"operation":"coverage","coverage":{"repository":str(root),"artifact":str(artifact)}})
    assert coverage["status"] == "collected" and coverage["coverage"]["files"], coverage
