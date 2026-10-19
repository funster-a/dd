#!/usr/bin/env python3
# Кэш на краю (ADR 031, исправление 19.10.2026): сколько запросов занятости
# доходит до api после паузы, сразу после истечения ответа и под постоянной
# нагрузкой. Запускается на поднятом docker compose с демо-данными:
#
#   python3 loadtest/edgecache.py
#
# Запросы идут через балансировщик (127.0.0.1:8080), каждый сценарий — на свой
# адрес (?probe=...), чтобы не мешать соседям. Число запросов в api считается
# по dd_http_request_duration_seconds_count маршрута занятости на всех
# экземплярах api, X-Cache показывает, что ответил край.
import http.client
import subprocess
import sys
import threading
import time
import urllib.request
import uuid
from collections import Counter

EDGE = ('127.0.0.1', 8080)
ROUTE = '/v1/events/{eventID}/availability'


def sh(*cmd):
    return subprocess.check_output(cmd, text=True).strip()


def api_addresses():
    ids = sh('docker', 'compose', 'ps', '-q', 'api').split()
    return [sh('docker', 'inspect', '-f', '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}', i) for i in ids]


def demo_event():
    return sh('docker', 'compose', 'exec', '-T', 'postgres', 'psql', '-U', 'dd', '-d', 'dd', '-tAc',
              "SELECT e.id FROM events e JOIN organizers o ON o.id = e.organizer_id WHERE o.slug = 'demo' AND e.slug = 'standup'")


def api_requests(addrs):
    n = 0.0
    for a in addrs:
        for line in urllib.request.urlopen(f'http://{a}:8080/metrics', timeout=3).read().decode().splitlines():
            if line.startswith('dd_http_request_duration_seconds_count') and f'route="{ROUTE}"' in line:
                n += float(line.rsplit(' ', 1)[1])
    return int(n)


def get(conn, path):
    conn.request('GET', path)
    r = conn.getresponse()
    r.read()
    return r.getheader('X-Cache')


def burst(path, n):
    conns = [http.client.HTTPConnection(*EDGE, timeout=30) for _ in range(n)]
    for c in conns:
        c.connect()
    start, seen, lock = threading.Barrier(n), Counter(), threading.Lock()

    def one(c):
        start.wait()
        status = get(c, path)
        with lock:
            seen[status] += 1

    threads = [threading.Thread(target=one, args=(c,)) for c in conns]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return dict(seen)


def scenario(name, path_base, addrs, pause, n=200):
    path = f'{path_base}?probe={uuid.uuid4().hex[:8]}'
    if pause is not None:
        get(http.client.HTTPConnection(*EDGE), path)
        time.sleep(pause)
    before = api_requests(addrs)
    seen = burst(path, n)
    time.sleep(0.5)
    print(f'{name:<32} {n} одновременных: {seen}, запросов в api: {api_requests(addrs) - before}')


def steady(path_base, addrs, seconds=10, workers=20):
    path = f'{path_base}?probe={uuid.uuid4().hex[:8]}'
    seen, lock, stop = Counter(), threading.Lock(), time.time() + seconds
    before = api_requests(addrs)

    def loop():
        c = http.client.HTTPConnection(*EDGE, timeout=30)
        while time.time() < stop:
            status = get(c, path)
            with lock:
                seen[status] += 1

    threads = [threading.Thread(target=loop) for _ in range(workers)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    time.sleep(0.5)
    print(f'{"нагрузка " + str(seconds) + " с, " + str(workers) + " потоков":<32} {dict(seen)}, запросов в api: {api_requests(addrs) - before}')


def main():
    event = sys.argv[1] if len(sys.argv) > 1 else demo_event()
    addrs = api_addresses()
    base = f'/v1/events/{event}/availability'
    for i in range(3):
        scenario(f'после паузы 8 с, прогон {i + 1}', base, addrs, 8)
    scenario('сразу после истечения (3 с)', base, addrs, 3)
    scenario('новый адрес', base, addrs, None)
    steady(base, addrs)


if __name__ == '__main__':
    main()
