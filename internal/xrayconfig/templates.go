package xrayconfig

func DefaultDefinition() Definition {
	return JSONDefinition{Raw: []byte(defaultTemplate)}
}

const defaultTemplate = `{
  "log": {
    "loglevel": "info"
  },
  "api": {
    "tag": "api",
    "listen": "127.0.0.1:10086",
    "services": ["HandlerService", "StatsService"]
  },
  "inbounds": [
    {
      "tag": "xhttp-vless",
      "listen": "/dev/shm/xray.sock,0666",
      "protocol": "vless",
      "settings": {
        "clients": [],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "xhttp",
        "xhttpSettings": {
          "mode": "auto",
          "path": "/split"
        }
      }
    }
  ],
  "outbounds": [
    {
      "tag": "direct",
      "protocol": "freedom",
      "settings": {}
    },
    {
      "tag": "blocked",
      "protocol": "blackhole",
      "settings": {}
    }
  ],
  "routing": {
    "domainStrategy": "AsIs",
    "rules": [
      {
        "type": "field",
        "ip": [
          "geoip:private"
        ],
        "outboundTag": "blocked"
      }
    ]
  },
  "stats": {}
}`
