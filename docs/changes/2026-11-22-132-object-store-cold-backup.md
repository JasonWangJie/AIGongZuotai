# 132 · V18-A 版本化对象存储停机冷备与独立还原

## 目的

在源对象服务退出后，验证完整服务目录的冷备能恢复原对象版本。工作台的存储身份包含 endpoint、region、bucket 和 prefix；恢复实例继续使用相同的客户端定位。按先前由服务生成的 VersionId 读回原字节，才能核对旧工作台收据。

## 范围

2026-11-22，关联 V18-A、H04 资产与交付、H07 可靠性与性能、H09 工程与交付治理。本项运行于 Windows 回环环境，固定本机 SeaweedFS 4.53 的官方二进制摘要。对象服务、版本化桶、凭据和持久目录均为本轮新建。演练未连接 PostgreSQL；工作台、数据库、私有目录和密钥的联合还原由集成人另外验收。

## 实际文件

- [对象服务演练控制程序](../../scripts/recovery/object-store-drill/drill.py) 提供启动、写入探针版本、冷备、从备份还原、核验和停止。它校验二进制 SHA-256、进程路径、PID 和创建时间，八个监听必须全部位于回环地址。每个演练目录独立加锁，同名快照或非空新目录保留原内容。
- [S3 版本探针](../../scripts/recovery/object-store-drill/s3probe/main.go) 使用仓库已锁定的 AWS Go SDK，对独立新桶启用版本控制，保存每次 PUT 返回的原 VersionId。恢复过程只读取既有桶的版本状态；指定版本读回应同时满足 VersionId、大小和 SHA-256。

## 接口与数据影响

控制程序以 `python scripts/recovery/object-store-drill/drill.py <action> --root <新演练目录>` 调用，首次 `start` 另需 `--weed <已核对的本地 weed.exe>`。它生成私有 `s3.json`，提供 `TEST_S3_ENDPOINT`、`TEST_S3_BUCKET`、`TEST_S3_REGION`、`TEST_S3_ACCESS_KEY_ID` 和 `TEST_S3_SECRET_ACCESS_KEY`。工作台应沿用自己的 prefix。

1. `start` 创建 `source` 并启动具专用身份的版本化对象服务。可通过 `seed` 将同一键的两个不同字节版本与服务器原 VersionId 写入 `versions.json`。
2. 停止工作台写入后执行 `backup`。它通知专属隐藏控制台优雅退出，等待源进程实际终止，再将含卷、主节点状态、filer 元数据及服务身份配置的完整目录复制到 `backup`。文件相对路径、长度和 SHA-256 在复制前后逐项核对。
3. `restore` 将备份逐文件核验后复制到新的 `restored` 目录，并以原服务端口、原桶和原身份启动新进程。端口不能独占时退出。源目录和冷备副本保留。
4. `verify` 读取原版本清单。调用方可传 `--versions <manifest.json>`，格式为包含 `key`、`version_id`、`sha256`、`size` 的对象数组。最后 `stop` 仅停止本次演练目录所记录且已核验身份的服务。

## 验证结果

- 初次用离线构建强制禁用 Go 校验库，已安装工具链因无法验证而拒绝构建。改为仅禁用模块代理，沿用本地工具链校验后构建成功。最初从主目录执行了独立探针，随后按批次归属将两个源文件移入 `.runtime/v18a-preparation/scripts/recovery/object-store-drill`，并在全新 `staged-round` 下使用该暂存入口重做全链。
- 最终暂存版本按 `start → seed → backup → restore → verify → stop` 实际执行通过。源进程 PID 10072，恢复进程 PID 32704，客户端均使用 `127.0.0.1:61892`；两次启动均核实八个回环监听，并确认两个进程分别退出。冷备共 69 个文件、18056 字节。冷备清单 SHA-256 为 `b1759ba98e007eb0649a44745336f10030a01929e262b1101f1ff1a7c7e72386`。
- 同一 key 的两个服务原 VersionId 不同。还原后指定版本分别读出 1027、5141 字节，SHA-256 分别为 `11fc2897455d17673ba8bf59a1f5973567f7eace78e5749faa32681015c9a6e2`、`6db116a68eaf2f3e668f76a73de3c7ea0688d0102ccdbff25f45096c03ef7387`。篡改预期 SHA 的独立清单被拒绝。原始去敏结果在 `.runtime/v18a-preparation/staged-round/verification-report.json`。

## 风险与限制

源、备份和恢复位于同一物理 Windows 主机。该结果验证本机对象服务冷备，跨主机灾备、跨区域复制、云服务 IAM、生产吞吐和停机时长另验。调用方负责先停工作台写入、保存一致时点的数据库和所需密钥。服务身份包含在完整冷备中，演练目录及其子目录均需按私有数据保存。

## 回滚方式

用相同 `--root` 执行 `stop`，核对退出记录。源数据、独立冷备、恢复副本和原版本清单都保留在对应的演练目录中。现有对象服务和业务环境继续使用各自的端点与凭据。

## 文档同步

集成人在 V18-A 联合验收后同步[根 README](../../README.md)、[对象存储 Wiki](../wiki/object-storage.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成的 TODO 及[推进记录](../planning/progress.md)。
