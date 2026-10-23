#!/usr/bin/python3
"""A real DNS datagram through the local Xray SOCKS UDP association."""
import os
import socket
import struct

with socket.create_connection(('127.0.0.1', 10888), timeout=8) as tcp:
    tcp.sendall(b'\x05\x01\x00')
    assert tcp.recv(2) == b'\x05\x00'
    tcp.sendall(b'\x05\x03\x00\x01' + b'\x00' * 6)
    response = tcp.recv(32)
    assert response[:4] == b'\x05\x00\x00\x01', response[:4]
    relay = (socket.inet_ntoa(response[4:8]), struct.unpack('!H', response[8:10])[0])
    if relay[0] == '0.0.0.0':
        relay = ('127.0.0.1', relay[1])
    ident = os.urandom(2)
    query = ident + b'\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00' + b'\x07example\x03com\x00\x00\x01\x00\x01'
    packet = b'\x00\x00\x00\x01' + socket.inet_aton('223.5.5.5') + struct.pack('!H', 53) + query
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as udp:
        udp.settimeout(15)
        udp.sendto(packet, relay)
        reply, _ = udp.recvfrom(4096)
        assert reply[:4] == b'\x00\x00\x00\x01'
        dns = reply[10:]
        assert dns[:2] == ident and dns[3] & 15 == 0
        assert struct.unpack('!H', dns[6:8])[0] > 0
        print('PASS: DNS UDP reply through Windows Xray -> VPS -> N1')
