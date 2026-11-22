# 0028 · 对新付费生成执行工作区额度风控

每个工作区有两个阈值，分别限制当前未结冻结额和每个 UTC 整分钟内新受理的有价生成数量。已有钱包的冻结余额包含待核实的预约，撤销或审核隔离也可能继续占用。使用原始预约计算频率，一次原键回放不会额外占用次数。单次审批只允许同一人显式提交仍有效的同一原报价。

## 停写配置

`cmd/risk-policy` 使用调用进程的 `DATABASE_URL`，与工作台共用控制写锁及精确数据库格式检查。默认动作为 `inspect`。以下命令用于查看和预演。工作区 ID、操作键和证据号由操作人填入。示例拟设的限额为 5 CREDIT、每分钟 8 单。

```powershell
go run ./cmd/risk-policy -workspace $workspaceId
go run ./cmd/risk-policy -action set -workspace $workspaceId -max-held-micros 5000000 -max-per-minute 8 -operation-key $operationKey -reason policy_review -evidence $ticketId
```

查看返回的 `policy_change.before` 和 `policy_change.after`。确认后在原命令追加 `-apply -plan-digest $digest`。需要关闭时用新操作键预演 `-action disable`，省略两个阈值。每个已提交的操作保留原收据。重新读取早期的设置收据不会重新启用已关闭的规则。

## 历史与恢复

028 为新报价保存实际申请人，旧报价保留既有数据。缺少原申请人证据的报价不能获颁个人豁免。升级前联备数据库、私有目录及 owner、相关密钥和对象版本。恢复时需一起核对规则版本、原报价、批准、核销、预约及实际交付。[实施范围](../../planning/v14c3-contract.md)
