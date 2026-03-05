### MASQUERADE I

Tool to round-robin proxy via SSH tunnels.

<img src="static/msqi.svg" width="200" align="right"> 

Original idea (with iptables) is taken from https://github.com/blacklanternsecurity/TREVORproxy and implemented in Go
as a part of my practice.
The idea is to create spin up `ssh -D` and redirect each nth packet to 'em with iptables statisctic

```bash
sudo SSH_AUTH_SOCK=$SSH_AUTH_SOCK ./main proxy -p 1337 -s ssh1@proxy1 -s ssh@proxy2
```

```bash
Spinning up SSH Tunnel to root@[EDITED] ...
Spinning up SSH Tunnel to root@[EDITED] ...
Creating iptables chain MSQI8179
Applying iptables rule [-t nat -A MSQI8179 -d 127.0.0.1 -o lo -p tcp --dport 1337 -j DNAT --to-destination 127.0.0.1:13370 -m statistic --mode nth --every 2 --packet 0] ...
Applying iptables rule [-t nat -A MSQI8179 -d 127.0.0.1 -o lo -p tcp --dport 1337 -j DNAT --to-destination 127.0.0.1:13371] ...
^C
Cleaning up iptables rules all the shit...
2026/03/04 21:24:44 Cleaning up PID -7032
2026/03/04 21:24:44 Cleaning up PID -7033
```

TODO:

- [ ] Fix killing SSH (it's not working lol)
- [ ] Test with more than 2 proxies