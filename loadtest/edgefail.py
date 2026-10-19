#!/usr/bin/env python3
# Кэш на краю при отказе api (ADR 031, исправление 19.10.2026): что получают
# покупатели, когда запись устарела, а api отвечает 503 или завис.
#
#   python3 loadtest/edgefail.py [конфиг края]    # по умолчанию deploy/nginx/api-lb.conf
#
# Скрипт поднимает поддельный api на 127.0.0.1:18099 и nginx из того же образа,
# что в Compose, с переданным конфигом края (адрес api подменяется на
# поддельный). Нужен Docker с сетью host, то есть Linux.
import http.client
import http.server
import re
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
from collections import Counter

UPSTREAM, EDGE = 18099, 18092
IMAGE = 'nginx:1.29-alpine'
state = {'mode': 'ok', 'hits': 0, 'version': 0}


class FakeAPI(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        state['hits'] += 1
        if state['mode'] == 'hang':
            time.sleep(80)  # дольше proxy_read_timeout любого варианта
            return
        if state['mode'] == '503':
            # как httpx.WriteUnavailable: 503 без Cache-Control
            body = b'{"error":{"code":"unavailable"}}'
            self.send_response(503)
            self.send_header('Retry-After', '2')
        else:
            state['version'] += 1
            body = ('{"version":%d}' % state['version']).encode()
            self.send_response(200)
            self.send_header('Cache-Control', 'public, max-age=2')
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def edge_config(path):
    conf = open(path, encoding='utf-8').read()
    conf = conf.replace('listen 8080;', f'listen {EDGE};')
    conf = re.sub(r'set \$api http://[^;]+;', f'set $api http://127.0.0.1:{UPSTREAM};', conf)
    return re.sub(r'\n\s*resolver [^\n]*', '', conf)


def get(path):
    start = time.time()
    c = http.client.HTTPConnection('127.0.0.1', EDGE, timeout=120)
    c.request('GET', path)
    r = c.getresponse()
    r.read()
    return r.status, r.getheader('X-Cache'), time.time() - start


def wave(path, n, seconds):
    out, lock = [], threading.Lock()

    def one():
        res = get(path)
        with lock:
            out.append(res)

    threads = []
    for _ in range(n):
        t = threading.Thread(target=one)
        t.start()
        threads.append(t)
        time.sleep(seconds / n)
    for t in threads:
        t.join()
    return out


def main():
    conf_path = sys.argv[1] if len(sys.argv) > 1 else 'deploy/nginx/api-lb.conf'
    server = http.server.ThreadingHTTPServer(('127.0.0.1', UPSTREAM), FakeAPI)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    with tempfile.NamedTemporaryFile('w', suffix='.conf', delete=False) as f:
        f.write(edge_config(conf_path))
    name = f'edgefail-{int(time.time())}'
    subprocess.run(['docker', 'run', '-d', '--rm', '--name', name, '--network', 'host',
                    '-v', f'{f.name}:/etc/nginx/conf.d/default.conf:ro', IMAGE], check=True, stdout=subprocess.DEVNULL)
    try:
        for _ in range(50):
            try:
                urllib.request.urlopen(f'http://127.0.0.1:{EDGE}/lb-healthz', timeout=1)
                break
            except OSError:
                time.sleep(0.2)
        print(f'конфиг края: {conf_path}')
        for mode, n in (('503', 50), ('hang', 30)):
            path = f'/v1/events/edgefail-{mode}-{time.time_ns()}/availability'
            state['mode'] = 'ok'
            get(path)
            time.sleep(3)  # запись устарела: max-age=2
            state['mode'], before = mode, state['hits']
            res = wave(path, n, 1.0)
            state['mode'] = 'ok'
            seen = dict(Counter((status, cache) for status, cache, _ in res))
            print(f'api {mode:<4}: {n} запросов за 1 с: {seen}, дольше всех ждал {max(w for _, _, w in res):.1f} с, запросов в api {state["hits"] - before}')
        path = f'/v1/events/edgefail-ok-{time.time_ns()}/availability'
        get(path)
        time.sleep(4)
        status, cache, _ = get(path)
        print(f'api ok  : первый запрос после паузы 4 с: {status} {cache}')
    finally:
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        server.shutdown()


if __name__ == '__main__':
    main()
