"""Loopback-only byte relay: SSH authentication stays on the local device.
One accepted agent socket consumes one preconnected device socket.
"""
import hashlib
import pathlib
import argparse
import json
import os
import queue
import select
import socket
import threading

parser = argparse.ArgumentParser()
parser.add_argument('--state', required=True)
args = parser.parse_args()
waiting = queue.Queue(maxsize=16)


def listen():
    sock = socket.socket()
    sock.bind(('127.0.0.1', 0))
    sock.listen(64)
    return sock


bridges, agents = listen(), listen()
state = {'bridge_port': bridges.getsockname()[1], 'agent_port': agents.getsockname()[1], 'pid': os.getpid(), 'revision': hashlib.sha256(pathlib.Path(__file__).read_bytes()).hexdigest()}
with open(args.state + '.tmp', 'w') as f:
    json.dump(state, f)
os.replace(args.state + '.tmp', args.state)


def receive_bridges():
    while True:
        conn, _ = bridges.accept()
        # Reconnects and health probes leave EOF sockets behind. Prune them
        # on arrival rather than waiting for an agent to consume the queue;
        # otherwise a full stale queue rejects every replacement bridge.
        live = []
        for _ in range(waiting.qsize()):
            try:
                candidate = waiting.get_nowait()
            except queue.Empty:
                break
            readable, _, _ = select.select([candidate], [], [], 0)
            if readable:
                candidate.close()
            else:
                live.append(candidate)
        for candidate in live:
            waiting.put_nowait(candidate)
        try:
            waiting.put_nowait(conn)
        except queue.Full:
            conn.close()


def forward(agent):
    bridge = None
    try:
        # Discard idle sockets whose device disconnected.
        while bridge is None:
            candidate = waiting.get(timeout=15)
            readable, _, _ = select.select([candidate], [], [], 0)
            if readable:
                candidate.close()
            else:
                bridge = candidate
        bridge.sendall(b'F')  # device opens a local SSH connection only on demand
        sockets = [agent, bridge]
        while sockets:
            readable, _, _ = select.select(sockets, [], [], 600)
            if not readable:
                break
            for source in readable:
                target = bridge if source is agent else agent
                data = source.recv(65536)
                if not data:
                    sockets.remove(source)
                    target.shutdown(socket.SHUT_WR)
                else:
                    target.sendall(data)
    except (OSError, queue.Empty):
        pass
    finally:
        agent.close()
        if bridge:
            bridge.close()
        capacity.release()


threading.Thread(target=receive_bridges, daemon=True).start()
# Matches the device side: long-lived connections must not starve new ones.
capacity = threading.BoundedSemaphore(64)
while True:
    agent, _ = agents.accept()
    if capacity.acquire(blocking=False):
        threading.Thread(target=forward, args=(agent,), daemon=True).start()
    else:
        agent.close()
