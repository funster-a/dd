#!/usr/bin/env python3
# Кэш на краю при сбоях api (ADR 031, исправление 19.10.2026): что получают
# покупатели, когда запись устарела, а api отвечает 503, завис, обрывает
# соединение без ответа или один экземпляр не принимает соединения.
#
#   python3 loadtest/edgefail.py [конфиг края]    # по умолчанию deploy/nginx/api-lb.conf
#
# Скрипт поднимает два поддельных экземпляра api (127.0.0.1:18099 и :18098) и
# nginx из того же образа, что в Compose, с переданным конфигом края. Адрес api
# подменяется на эти два экземпляра без max_fails: и в `set $api`, и в строке
# `server ... resolve` блоков upstream. Разрешение имени через DNS стенд не
# проверяет. Нужен Docker с сетью host, то есть Linux. Подходят
# deploy/nginx/api-lb.conf и шаблон deploy/k8s/base/edge.conf.template.
import http.client
import http.server
import os
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
from collections import Counter

PODS, EDGE = (18099, 18098), 18092
IMAGE = 'nginx:1.29-alpine'
CLOSE_AFTER = 4  # обрыв без ответа: так api закрывает соединение, когда обработчик дольше WriteTimeout
state = {'mode': 'ok', 'hits': 0, 'version': 0}


class FakeAPI(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        state['hits'] += 1
        if state['mode'] == 'hang':
            time.sleep(80)  # дольше proxy_read_timeout любого варианта
            return
        if state['mode'] == 'close':
            time.sleep(CLOSE_AFTER)
            self.close_connection = True
            return
        if state['mode'] == 'slow':
            time.sleep(CLOSE_AFTER)
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


def edge_config(path, run):
    conf = open(path, encoding='utf-8').read()
    conf = conf.replace('listen 8080;', f'listen {EDGE};')
    conf = re.sub(r'set \$api http://[^;]+;', 'set $api http://edgefail_api;', conf)
    # /lb-healthz отвечает именем прогона: так видно, что отвечает наш nginx,
    # а не оставшийся от прерванного прогона.
    conf = conf.replace('return 200 "ok\\n";', f'return 200 "{run}\\n";')
    conf = re.sub(r'\n\s*resolver [^\n]*', '', conf)
    servers = ' '.join(f'server 127.0.0.1:{p} max_fails=0;' for p in PODS)
    conf = re.sub(r'server [^;\s]+ resolve[^;]*;', servers, conf)
    return f'upstream edgefail_api {{ {servers} }}\n' + conf


def port_busy(port):
    with socket.socket() as s:
        return s.connect_ex(('127.0.0.1', port)) == 0


def get(path):
    start = time.time()
    c = http.client.HTTPConnection('127.0.0.1', EDGE, timeout=200)
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


def report(label, res, hits):
    seen = dict(Counter((status, cache) for status, cache, _ in res))
    print(f'{label:<34} {len(res)} запросов: {seen}, дольше всех ждал {max(w for _, _, w in res):.1f} с, запросов в api {hits}')


def stale_scenario(mode, n):
    path = f'/v1/events/edgefail-{mode}-{time.time_ns()}/availability'
    state['mode'] = 'ok'
    get(path)
    time.sleep(3)  # запись устарела: max-age=2
    state['mode'], before = mode, state['hits']
    res = wave(path, n, 1.0)
    if mode == 'close':
        # Обновление в фоне и повтор на втором экземпляре идут уже после
        # ответа покупателям: ждём их, чтобы сосчитать.
        time.sleep(2 * CLOSE_AFTER + 1)
    state['mode'] = 'ok'
    return res, state['hits'] - before


def main():
    conf_path = sys.argv[1] if len(sys.argv) > 1 else 'deploy/nginx/api-lb.conf'
    # Порты поддельного api держит только живой прогон: его не трогаем.
    for port in PODS:
        if port_busy(port):
            sys.exit(f'порт {port} занят: идёт другой прогон или другая программа')
    # Без живого прогона контейнеры edgefail-* — остатки прерванных, они держат порт края.
    try:
        stale = subprocess.run(['docker', 'ps', '-aq', '--filter', 'name=^edgefail-'],
                               capture_output=True, text=True, check=True).stdout.split()
    except subprocess.CalledProcessError as e:
        sys.exit(f'docker недоступен: {e.stderr.strip()}')
    if stale:
        subprocess.run(['docker', 'rm', '-f', *stale], stdout=subprocess.DEVNULL, check=True)
    if port_busy(EDGE):
        sys.exit(f'порт {EDGE} занят другой программой')
    # SIGTERM и SIGHUP тоже проходят через finally: контейнер не остаётся.
    # Под nohup SIGHUP игнорируется, и так и остаётся.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
    if signal.getsignal(signal.SIGHUP) is not signal.SIG_IGN:
        signal.signal(signal.SIGHUP, lambda *_: sys.exit(1))
    servers = [http.server.ThreadingHTTPServer(('127.0.0.1', p), FakeAPI) for p in PODS]
    for s in servers:
        threading.Thread(target=s.serve_forever, daemon=True).start()
    name = f'edgefail-{int(time.time())}'
    conf_file = None
    try:
        with tempfile.NamedTemporaryFile('w', suffix='.conf', delete=False) as f:
            conf_file = f.name
            f.write(edge_config(conf_path, name))
        os.chmod(conf_file, 0o644)
        # Без --rm: если nginx не поднимется, его лог ещё можно прочитать.
        subprocess.run(['docker', 'run', '-d', '--name', name, '--network', 'host',
                        '-v', f'{conf_file}:/etc/nginx/conf.d/default.conf:ro', IMAGE], check=True, stdout=subprocess.DEVNULL)
        seen_body = None
        for _ in range(50):
            try:
                seen_body = urllib.request.urlopen(f'http://127.0.0.1:{EDGE}/lb-healthz', timeout=1).read().decode().strip()
                if seen_body == name:
                    break
            except OSError:
                pass
            time.sleep(0.2)
        else:
            logs = subprocess.run(['docker', 'logs', '--tail', '20', name], capture_output=True, text=True).stderr.strip()
            why = f'/lb-healthz ответил {seen_body!r}, а не {name!r}' if seen_body is not None else 'край не ответил'
            sys.exit(f'nginx с конфигом {conf_path} не поднялся: {why}\n{logs}')
        print(f'конфиг края: {conf_path}')

        report('api отвечает 503', *stale_scenario('503', 50))
        report('api завис', *stale_scenario('hang', 30))
        report(f'api обрывает соединение через {CLOSE_AFTER} с', *stale_scenario('close', 10))

        # Покупатель, который обновляет запись, уходит через секунду, а api
        # отвечает за CLOSE_AFTER: попадёт ли ответ в кэш для следующего.
        path = f'/v1/events/edgefail-abort-{time.time_ns()}/availability'
        get(path)
        time.sleep(3)
        state['mode'], before = 'slow', state['hits']
        c = socket.create_connection(('127.0.0.1', EDGE))
        c.sendall(f'GET {path} HTTP/1.1\r\nHost: edge\r\n\r\n'.encode())
        time.sleep(1)
        c.close()
        time.sleep(CLOSE_AFTER + 0.5)
        state['mode'] = 'ok'
        status, cache, wait = get(path)
        print(f'{"обновлявший ушёл через 1 с":<34} следующий: {status} {cache}, ждал {wait:.1f} с, запросов в api {state["hits"] - before}')

        # Один экземпляр не принимает соединения: запросы на новые адреса.
        servers[1].shutdown()
        servers[1].server_close()
        before = state['hits']
        res = [get(f'/v1/events/edgefail-refused-{time.time_ns()}/availability') for _ in range(10)]
        report('один экземпляр не принимает', res, state['hits'] - before)

        path = f'/v1/events/edgefail-ok-{time.time_ns()}/availability'
        get(path)
        time.sleep(4)
        status, cache, _ = get(path)
        print(f'{"первый запрос после паузы 4 с":<34} {status} {cache}')

        # Не принимает ни один экземпляр: запись устарела, нового ответа взять негде.
        time.sleep(3)
        servers[0].shutdown()
        servers[0].server_close()
        res = [get(path) for _ in range(5)]
        report('не принимает ни один экземпляр', res, 0)
    finally:
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for s in servers:
            s.shutdown()
            s.server_close()
        if conf_file:
            os.unlink(conf_file)


if __name__ == '__main__':
    main()
