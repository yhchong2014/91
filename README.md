# 91

<p align="center">
  <img width="120" height="120" alt="91" src="https://github.com/user-attachments/assets/5b323c94-bbd3-4dce-bbc8-adc86935b7de" />
</p>

<p align="center">
  😄 个人私有视频站 😄
</p>

<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="#功能特性">功能特性</a> ·
  <a href="#预览图">预览图</a> ·
  <a href="#数据存放位置">数据目录</a> ·
  <a href="#许可证">许可证</a>
</p>


## 功能特性

- **多后端支持** — 兼容 115 云盘、PikPak 云盘、123网盘、联通网盘、光鸭网盘、OneDrive、Google Drive 和本地存储
- **低带宽播放** — 115 云盘、PikPak 云盘、123网盘、联通网盘、光鸭网盘、OneDrive 支持302模式，在线播放视频时，不占用服务器带宽，播放体验不受服务器带宽影响；Google Drive 不支持302模式，走服务器中转，观看体验会受服务器带宽影响
- **封面 & 预览片段** — 自动为每个视频生成封面图和预览片段，首页快速选片
- **爬虫脚本** — 项目支持导入自定义脚本，但是有一些规范，具体可以参考 [SpiderFor91](https://github.com/Just-Spider/SpiderFor91)，项目不再内置任何爬虫脚本
- **短视频模式** — 一键切换抖音风格，沉浸刷片

## 预览图

### 电脑端

<p>
  <img width="49%" alt="首页" src="https://github.com/user-attachments/assets/9808fceb-760b-4dd5-b7d2-8622b95b90d5" />
  <img width="49%" alt="播放页" src="https://github.com/user-attachments/assets/859db4aa-1fba-44f2-bb46-1db07c2f964f" />
</p>

<p>
  <img width="49%" alt="主题切换" src="https://github.com/user-attachments/assets/96bea37a-8764-413e-9b70-1856b4ae0cd2" />
  <img width="49%" alt="管理页" src="https://github.com/user-attachments/assets/29c1e27a-7651-4dfc-93dd-556331844214" />
</p>

### 手机端

<p align="center">
  <img width="1284" height="1134" alt="手机端" src="https://github.com/user-attachments/assets/bdb7a86c-a4e5-483e-a307-e02c0bb34dac" />
</p>

## 快速开始

### 方式一：一键安装脚本（推荐）

```bash
sudo apt update && sudo apt install -y curl ca-certificates
curl -fsSL https://raw.githubusercontent.com/nianzhibai/91/main/install.sh -o install.sh
sudo bash install.sh
```

部署完成后访问：

| 地址 | 说明 |
|------|------|
| `http://服务器IP:9191/` | 前台 |
| `http://服务器IP:9191/admin` | 后台管理 |

**注意：如果首次访问，显示502，可以运行 `91 restart` 重启一下服务**

安装后自动注册 `91` 管理命令：

```bash
91            # 打开管理菜单
91 status     # 查看运行状态
91 logs       # 查看日志
91 update     # 更新到最新版本
91 restart    # 重启服务
91 stop       # 停止服务
```

**已部署用户升级：**

```bash
91 update
```

### 使用 Nginx 反向代理注意
nginx配置中需要添加下面字段
```nginx
    location / {
        proxy_pass http://127.0.0.1:当前项目监听端口;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
```
如果不配置，可能导致自己被封禁


### 方式二：Docker Compose 部署

**1. 准备目录**

```bash
mkdir -p video-site-91 && cd video-site-91
```

**2. 创建 `docker-compose.yml`**

```yaml
services:
  video-site-91:
    image: ghcr.io/nianzhibai/91:stable
    container_name: video-site-91
    ports:
      - "9191:9191"
    volumes:
      - ./data:/opt/video-site-91/data
    restart: unless-stopped
```
创建yml文件后运行下面指令
```bash
docker compose pull
docker compose up -d
```

如果想固定某个 Release 版本，可以改成明确的 tag，例如：

```yaml
image: ghcr.io/nianzhibai/91:v0.0.6
```

或直接拉取仓库内置配置：

```bash
curl -fsSL https://raw.githubusercontent.com/nianzhibai/91/main/docker-compose.yml -o docker-compose.yml
```

**3. 启动**

```bash
docker compose up -d
```

**常用命令：**

```bash
docker compose logs -f       # 查看日志
docker compose pull          # 拉取最新正式版 stable 镜像
docker compose up -d         # 更新并重启
```

> 所有配置、数据库、封面、预览及上传文件均保存在 `./data/` 目录下。
> 从旧版本升级 Docker 部署时，执行 `docker compose pull && docker compose up -d` 即可；`./data/` 不会被镜像更新覆盖。


## 数据存放位置

### 一键脚本部署

| 路径 | 内容 |
|------|------|
| `/opt/video-site-91/config.yaml` | 配置文件、管理员账号、网盘凭证 |
| `/opt/video-site-91/data/video-site.db` | SQLite 数据库 |
| `/opt/video-site-91/data/previews/` | 封面图和预览片段 |

### Docker Compose 部署

| 路径 | 内容 |
|------|------|
| `./data/config.yaml` | 配置文件、管理员账号、网盘凭证 |
| `./data/video-site.db` | SQLite 数据库 |
| `./data/previews/` | 封面图和预览片段 |
| `./data/uploads/` | 本地上传的视频文件 |


## 使用须知

本项目面向**个人私有部署**，请仅接入你有权访问和管理的内容，并遵守对应网盘、站点的服务条款及所在地法律法规。


## PR提交规范
欢迎大家提交PR，一起来完善这个项目，但是这里要说明一下PR提交的规范
1. 一个PR的功能改动要单一，不建议一个PR修改了大量功能。单个PR单个功能修改，这样也更容易Merge
2. 完善项目的PR比新增功能的PR更容易Merge（例如：例如你发现开发者没有实现爬取的视频上传到某个网盘，并且你有这个需求，此时你可以实现一下这个功能然后提交PR，也感谢你为开发者分担工作量）
3. 新增功能的PR不容易Merge，因为某些功能的需求可能不是所有人都需要的，如果一味的不断增加功能，会让项目变得过于庞大。当然如果你肯定你的新功能和想法很好，并且相信将会对于项目有很大的改善，那么热烈欢迎你的PR


## 许可证

本项目基于 [MIT License](LICENSE) 开源


## 致谢

- [OpenList](https://github.com/OpenListTeam/OpenList) — 优秀的开源项目
- [LinuxDo](https://linux.do/) — 学 AI 上 L 站
- [NodeSeek](https://nodeseek.com/) — MJJ 上 N 站
