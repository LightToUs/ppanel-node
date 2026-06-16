
### 一键安装

```bash
wget -N https://raw.githubusercontent.com/lighttous/ppanel-node/main/scripts/install.sh && bash install.sh
```

## 构建
``` bash
GOEXPERIMENT=jsonv2 go build -v -o ./node -trimpath -ldflags "-s -w -buildid="
```

## update
``` bash
git add .
git commit -m "fix: more bug fixes"
./scripts/publish-new-repo-release.sh v1.2.1
```