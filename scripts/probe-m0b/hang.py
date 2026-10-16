#!/usr/bin/env python3
"""只 accept、不应答的本地监听器(M0b hang 模式)。用法:hang.py <port>"""
import socket, sys, threading, time

port = int(sys.argv[1]) if len(sys.argv) > 1 else 9
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", port))
srv.listen(16)
conns = []
while True:
    c, _ = srv.accept()
    conns.append(c)  # 持有连接,不读也不写
