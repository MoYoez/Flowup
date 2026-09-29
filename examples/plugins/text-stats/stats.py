"""A local Flowup action: one JSON request in, one JSON response out."""
import json
import sys


def main():
    request = json.loads(sys.stdin.buffer.read().decode("utf-8"))
    if request["protocol_version"] != 1:
        response = {"error": {"message": "Unsupported protocol version"}}
    else:
        text = request["input"]["text"]
        response = {
            "output": {
                "characters": len(text),
                "lines": len(text.splitlines()),
                "preview": text[:80],
            }
        }
    sys.stdout.buffer.write((json.dumps(response, ensure_ascii=False) + "\n").encode("utf-8"))


if __name__ == "__main__":
    main()
