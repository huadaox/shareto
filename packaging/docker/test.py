import hashlib
import http.client
import json
import os
import re
import socket
import subprocess
import tempfile
import uuid
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlencode, urlsplit


repository = Path(__file__).resolve().parents[2]
work = repository / "work"
work.mkdir(exist_ok=True)
fixture = Path(tempfile.mkdtemp(prefix="docker-test-", dir=work))
shared = fixture / "shared"
shared.mkdir(mode=0o755)
content = os.urandom(256 * 1024)
report = shared / "report.bin"
report.write_bytes(content)
report.chmod(0o644)
with socket.socket() as listener:
    listener.bind(("127.0.0.1", 0))
    port = listener.getsockname()[1]
base_url = f"http://127.0.0.1:{port}"
config = json.loads((repository / "packaging/docker/config.example.json").read_text())
config["public_base_url"] = base_url
config_file = fixture / "config.json"
config_file.write_text(json.dumps(config))
config_file.chmod(0o644)
project = "shareto-test-" + uuid.uuid4().hex[:12]
environment = dict(os.environ)
environment.update({
    "SHARETO_CONFIG_FILE": str(config_file),
    "SHARETO_FILES_DIR": str(shared),
    "SHARETO_BIND_ADDRESS": "127.0.0.1",
    "SHARETO_PORT": str(port),
    "SHARETO_IMAGE": project + ":test",
    "SHARETO_VERSION": "0.0.0-docker-test",
})
compose_command = ["docker", "compose", "--project-directory", str(repository),
                   "-f", str(repository / "compose.yaml"), "-p", project]


def run(arguments, *, check=True, input_text=None, timeout=120):
    result = subprocess.run(arguments, cwd=repository, env=environment,
                            input=input_text, text=True, capture_output=True,
                            timeout=timeout)
    if check and result.returncode != 0:
        raise RuntimeError(f"{arguments}:\n{result.stdout}\n{result.stderr}")
    return result


def compose(*arguments, **options):
    return run([*compose_command, *arguments], **options)


def cli(*arguments, **options):
    return compose("exec", "-T", "shareto", "shareto", *arguments, **options).stdout


def start():
    compose("up", "-d", "--no-build", "--wait", "--wait-timeout", "90")


def request(method, path, body=None, headers=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=15)
    connection.request(method, path, body=body, headers=headers or {})
    response = connection.getresponse()
    data = response.read()
    result = response.status, {key.lower(): value for key, value in response.getheaders()}, data
    connection.close()
    return result


def share(*arguments, input_text=None):
    output = cli(*arguments, "/files/report.bin", input_text=input_text)
    token_match = re.search(r"Token:\s+(\S+)", output)
    url_match = re.search(r"Public URL:\s+(\S+)", output)
    assert token_match and url_match, output
    token = token_match.group(1)
    assert url_match.group(1) == base_url + "/s/" + token, output
    return token, output


class FormFields(HTMLParser):
    def __init__(self):
        super().__init__()
        self.csrf = None

    def handle_starttag(self, tag, attrs):
        attributes = dict(attrs)
        if tag == "input" and attributes.get("name") == "csrf_token":
            self.csrf = attributes["value"]


try:
    compose("config", "--quiet")
    compose("build", "--pull", timeout=900)
    start()
    container = compose("ps", "-q", "shareto").stdout.strip()
    inspection = json.loads(run(["docker", "inspect", container]).stdout)[0]
    image = json.loads(run(["docker", "image", "inspect", environment["SHARETO_IMAGE"]]).stdout)[0]
    if os.environ.get("SHARETO_TEST_ARCH"):
        assert image["Architecture"] == os.environ["SHARETO_TEST_ARCH"]
    assert inspection["Config"]["User"] == "65532:65532"
    assert inspection["HostConfig"]["ReadonlyRootfs"]
    assert "ALL" in inspection["HostConfig"]["CapDrop"]
    assert "no-new-privileges:true" in inspection["HostConfig"]["SecurityOpt"]
    mounts = {mount["Destination"]: mount for mount in inspection["Mounts"]}
    assert not mounts["/files"]["RW"] and not mounts["/config/shareto/config.json"]["RW"]
    assert mounts["/data"]["RW"] and mounts["/data"]["Type"] == "volume"
    assert inspection["State"]["Health"]["Status"] == "healthy"
    assert "0.0.0-docker-test" in cli("version")
    unavailable = compose("exec", "-T", "-e", "XDG_RUNTIME_DIR=/unavailable",
                          "shareto", "shareto", "list", check=False)
    assert unavailable.returncode != 0, "健康检查命令没有报告连接失败"
    for destination in ("/files/forbidden.bin", "/forbidden.bin"):
        attempted_write = run(["docker", "cp", str(report), container + ":" + destination], check=False)
        assert attempted_write.returncode != 0, "只读目录允许写入：" + destination
    print("通过：真实容器启动、健康检查、非 root 用户、只读目录", flush=True)

    token, output = share("-m", "1", "-t", "10m")
    assert "Direct URL:" in output
    landing = "/s/" + token + "?raw=1"
    assert request("HEAD", landing)[0] == 200
    assert "1/1" in cli("list"), "HEAD 请求消耗了下载次数"
    status, headers, _ = request("GET", landing)
    assert status == 307
    download_path = urlsplit(headers["location"]).path
    status, _, downloaded = request("GET", download_path)
    assert status == 200 and downloaded == content
    status, _, partial = request("GET", download_path, headers={"Range": "bytes=128-383"})
    assert status == 206 and partial == content[128:384]
    assert request("GET", landing)[0] == 410
    print("通过：创建分享、完整下载、Range 下载、HEAD 和下载次数限制；SHA-256="
          + hashlib.sha256(downloaded).hexdigest(), flush=True)

    compose("restart", "shareto")
    start()
    assert request("GET", download_path)[2] == content
    assert request("GET", landing)[0] == 410
    compose("stop", "shareto")
    logs = compose("logs", "--no-color", "shareto").stdout
    assert "Daemon stopped cleanly." in logs
    compose("down")
    start()
    assert request("GET", download_path)[2] == content
    assert request("GET", landing)[0] == 410
    assert "0/1" in cli("list")
    cli("revoke", token)
    assert request("GET", landing)[0] == 404
    assert request("GET", download_path)[0] == 404
    print("通过：容器重启和删除后重建均保留分享、会话与剩余次数；撤销后链接失效", flush=True)

    password = uuid.uuid4().hex
    protected, output = share("--password-stdin", input_text=password + "\n")
    assert "Direct URL:" not in output
    protected_path = "/s/" + protected
    status, _, page = request("GET", protected_path + "?raw=1")
    assert status == 200 and page != content
    fields = FormFields()
    fields.feed(page.decode())
    assert fields.csrf
    form_headers = {"Content-Type": "application/x-www-form-urlencoded"}
    wrong = urlencode({"csrf_token": fields.csrf, "password": "incorrect-password"})
    assert request("POST", protected_path + "/claim", wrong, form_headers)[0] == 403
    correct = urlencode({"csrf_token": fields.csrf, "password": password})
    status, headers, _ = request("POST", protected_path + "/claim", correct, form_headers)
    assert status == 303
    assert request("GET", urlsplit(headers["location"]).path)[2] == content
    cli("revoke", protected)
    assert "No active shares." in cli("list")
    print("通过：密码保护、错误密码拒绝、正确密码下载、清理分享", flush=True)
finally:
    diagnostic = compose("logs", "--no-color", "shareto", check=False)
    (fixture / "container.log").write_text(diagnostic.stdout + diagnostic.stderr)
    compose("down", "--volumes", "--remove-orphans")
