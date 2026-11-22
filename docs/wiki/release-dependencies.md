# 工作台制品依赖与许可证记录

## 已核对的制品

034 清单记录 Windows Go 服务二进制和生产工作台目录。服务端从二进制内嵌的编译信息提取 Go 工具链版本与 20 个模块，逐项比对仓库 `go.sum`；Go 标准库与运行时另列一项。前端从已安装包与生产锁文件读取 22 个依赖，同时重做一次不落盘的 Vite 生产构建，把 33 个输出文件逐个与待核对目录比较。服务器和前端目录各有 SHA-256。[034 SPDX 清单](assets/workbench-034.spdx.json)

React、React DOM、scheduler、JSZip 和 PptxGenJS 在输出分块中有直接模块记录。其余生产锁依赖单列为已解析依赖，其中可能包含由上游包预先打包的内容、类型声明或 Node 端实现。开发依赖和测试框架不计入这个生产闭包。

## 许可证证据

对本机包根目录的 LICENSE、COPYING、NOTICE 等实际文件记录 SHA-256；没有此类文件时检查 README 中的许可证章节。npm 许可证表达式同时与锁文件核对。JSZip 的声明是 `(MIT OR GPL-3.0-or-later)`，pako 是 `(MIT AND Zlib)`，清单保留原表达式。isarray 的 MIT 文本在 README 中。https 这个 npm 包仅提供 ISC 声明，安装包里没有独立许可文本。项目自有服务和工作台未检出项目许可声明，清单保留 `NOASSERTION`。

本记录提供工程审查依据。发布负责人仍需按实际分发物核对许可文本和通知，确认项目自身的授权方式，并取得实际部署的 New API 分支、提交和二进制身份。PostgreSQL、对象存储服务、可选 FFmpeg/FFprobe、基础镜像和外置网关分别按实际部署归档。本报告没有检查外部服务的制品，也没有给出分发许可结论。

## 重新生成

先在仓库安装锁定依赖并构建服务和工作台，让当前 Go 工具链与二进制一致。为输出指定一个尚不存在的文件。命令只使用本地源码、已安装包、Go 模块缓存及已有制品。

```powershell
node apps/workbench/scripts/release-sbom.mjs --server .runtime/server.exe --dist apps/workbench/dist --output .runtime/workbench-new.spdx.json
```

输出遵循 SPDX 2.3 JSON。除时间与文档标识外，可比较制品、包版本、依赖关系和许可证证据。程序拒绝不相符的前端输出、未锁定的已观察包以及 Go 模块校验和差异。[SPDX 文档与包字段](https://spdx.github.io/spdx-spec/v2.3.1/package-information/) · [官方 JSON Schema](https://raw.githubusercontent.com/spdx/spdx-spec/v2.3/schemas/spdx-schema.json)

## 035 固定制品

035 服务 SHA-256 为 `09ba1436ccd0d96d03f2c93fe61a640ff6680b7f8e15a521e4ec9fb8ab083c54`，工作台目录树为 `9a08fe67be288aca9e43d3149f7f3a4fc2edf410ffdf7a14b4285e92204f06ff`。累计测试制品与生成清单的服务摘要、33 个前端文件逐字节一致。使用对应 Go 1.26.11 后，45 个包和 47 条关系通过离线官方 Schema、关系唯一性与可达性检查。 [035 SPDX 清单](assets/workbench-035.spdx.json)。

## 037 制品及压缩资源

037 的服务摘要绑定至同一版冻结源和 12 秒原库重开。静态树另覆盖每份 gzip 与原脚本的字节对应、锁定 npm 归档与实际安装文件，以及每条 SPDX 依赖关系。[完整核对](release-evidence-037.md)。
