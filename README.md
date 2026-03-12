# Ouroboros Scheduler 
**能量驱动的自适应并发调度引擎 (Energy-Based Adaptive Concurrency Scheduler)**

## 哲学与引言 (Philosophy & Introduction)

传统的 Goroutine 池或静态并发控制存在一个致命的架构缺陷：**它们依赖人类对系统的预判（静态参数配置）。** 当微服务遇到突发洪峰、底层 I/O 劣化或 CPU 缓存颠簸时，固定的并发上限会导致要么 CPU 极度闲置，要么系统因过度抢占（OS Scheduler Thrashing）而陷入死亡螺旋。

**队列长度是一个充满欺骗性的主观指标**（1000 个 `sleep` 任务与 10 个 `bcrypt` 散列任务的压力截然不同）。**唯有时间（执行延迟）才是衡量系统饱和度的客观真理。**

本项目是一个运行在用户态的**动态调度引擎 (Gatekeeper)**。它并不直接限制 Goroutine 的数量，而是通过 **AIMD Controller (TCP 风格的拥塞控制器)** 实时监听任务的 P99 执行延迟，并结合 **Energy-Based Priority Heap (能量优先级堆)**，在吞吐量与响应时间之间寻找完美的纳什均衡。

---

## 核心架构 (Core Architecture)

系统由四个高度解耦、机械同情（Mechanical Sympathy）的组件协同运转：

### 1. AIMD 并发控制器 (The Autonomic Nervous System)
借鉴 TCP 拥塞控制，将 CPU 视为网络带宽：
- **Additive-Increase (加法增长)**：当 P99 延迟处于健康水位时，线性增加并发槽位，持续探索硬件极限。
- **Multiplicative-Decrease (乘法衰减)**：一旦延迟突破 `TargetLatency` 阈值，立即以 $\beta$ 系数进行乘法收缩，并带有防抖冷却期（Jitter Protection），防止 STW GC 导致的误判。
- **Silent Saturation Detection**：静默饱和检测，防止长耗时任务导致无样本产出时，控制器陷入“盲区”。

### 2. 基于能量的统一调度模型 (Energy-Based Fairness)
抛弃绝对优先级带来的“低优先级饿死”问题，抽象出**能量 (Energy)** 模型。能量值越低，越早被调度出队。
> $E_i = \frac{R_i}{W_i} + \gamma \cdot \tau \cdot \log(C_i + 1) - \beta \cdot \tau \cdot \log(S_i + 1)$

- **$R_i$ (Runtime)**：累计运行时间。运行越久，能量越高（被迫让出 CPU）。
- **$W_i$ (Weight)**：用户设定的优先级（对数缩放）。高优先级权重极大，能量增长极缓慢。
- **$C_i$ (Creation Pressure)**：防突发惩罚。瞬间提交海量任务会获得极高的初始能量，防止洪峰淹没平稳流量。
- **$S_i$ (Yield Count)**：协作让步奖励。主动调用 `Yield()` 的任务会获得瞬时能量扣减。

### 3. 零分配的碎裂边界 (The Shattered Bottleneck: EnergyHeap)
为了突破全局锁在 16+ 核心下的扩展性瓶颈，我们将优先级堆分为 8 个独立的 Shard。
- **Power of Two Random Choices**：提取任务时，随机采样两个 Shard 并取其最优解。在保持 $O(1)$ 锁竞争开销的同时，获得了数学上趋近于全局排序的公平性。
- **Zero-Allocation**：底层数组直接操作具体类型指针，热路径（Hot Path）零逃逸、零 GC 压力。

### 4. 无锁紧急通道 (The L1 Dispatch Cache: FastQueue)
针对 `PriorityUltra` 级别的最高优任务，完全绕过能量堆的数学计算与锁机制，直接推入基于 Vyukov 算法的 **MPMC 有界环形队列**。
- 使用 `uint64` 序列号彻底消除 ABA 问题（数学边界推延至 58000 年后）。
- 修复了经典 Michael-Scott 队列的 "Dummy Node Leak" 内存泄漏漏洞。

---

## 快速上手 (Quick Start)

我们提供两种维度的交互 API，以满足不同场景对“研发心智”与“极致性能”的取舍。

### Approach A: 泛型闭包模式 (The Elegant Future)
**推荐业务开发使用。** 消除了结构体定义的样板代码，内置 Panic 隔离墙（Bloody Sincerity），以同步语义编写异步并发流。

```go
import "github.com/your_org/scheduler"

// 1. 初始化 Gatekeeper
cfg := scheduler.DefaultConfig()
gate := scheduler.New(cfg)
go gate.Start(context.Background())
defer gate.Wait()

// 2. 提交带返回值的任务 (泛型推断)
future, err := scheduler.SubmitFunc(gate, scheduler.PriorityHigh, func(ctx scheduler.Context) (string, error) {
    // 你的业务逻辑 ...
    
    // 协作式调度：主动让渡 CPU，累积协作奖励，防止长耗时任务被 Watchdog 惩罚
    if err := ctx.Yield(); err != nil {
        return "", err // Gatekeeper 关闭或上下文取消
    }
    
    return "Mission Accomplished", nil
})

// 3. 阻塞等待结果
result, err := future.Get(context.TODO())
fmt.Println(result)
```

对于无返回值的任务，可使用轻量级语法糖：
```go
futureVoid, _ := scheduler.SubmitVoid(gate, scheduler.PriorityNormal, func(ctx scheduler.Context) error {
    ctx.EnterSyscall()
    defer ctx.ExitSyscall() // 声明 I/O 阻塞，立即释放调度槽位给其他任务
    
    return doNetworkCall()
})
```

### 聚合屏障 (Concurrency Join)
执行 Fail-Fast（快速失败）的并发聚合。如果任何一个 Future 失败或 Panic，`Join` 会立刻中止并返回错误，同时释放其余未执行任务的槽位。

```go
f1, _ := scheduler.SubmitFunc(gate, 100, fetchUserData)
f2, _ := scheduler.SubmitFunc(gate, 100, fetchOrderData)

// 阻塞直到所有 Future 完成，或其中任意一个失败
results, err := scheduler.Join(ctx, f1, f2)
if err != nil {
    log.Printf("Barrier failed: %v", err)
}
```

### Approach B: 硬核接口模式 (The Hardcore Contract)
**推荐底层中间件/网关使用。** 闭包不可避免会产生极小的上下文变量内存逃逸。如果需要在 `for` 循环中每秒派发百万级任务且要求极致 Zero-Allocation，请直接实现 `Task` 接口。

```go
type CoreTask struct {
    ReqID string
}

func (t *CoreTask) Priority() int { return scheduler.PriorityUltra }

func (t *CoreTask) Execute(ctx scheduler.Context) error {
    // 生命周期完全封闭在结构体内部，无外部闭包捕获
    return process(t.ReqID)
}

// 直接提交，无额外封装开销
gate.Submit(&CoreTask{ReqID: "req-1024"})
```

---

## 核心机制指南 (Deep Dive)

### 1. I/O 阻塞与系统调用感知 (`EnterSyscall`)
Gatekeeper 的并发槽位极其珍贵。当任务需要执行网络请求、数据库查询等阻塞型 I/O 时，**必须**显式告知调度器，否则会导致系统假性饱和（CPU 闲置但槽位耗尽）。

```go
func (ctx scheduler.Context) Execute() error {
    // 离开 CPU 密集计算状态，进入阻塞态
    ctx.EnterSyscall() 
    
    // 执行耗时 I/O (此时该任务不再占用 Gatekeeper 的 active 槽位)
    res, err := http.Get("...") 
    
    // I/O 结束，重新排队申请 CPU 槽位（包含等待惩罚计算）
    if exitErr := ctx.ExitSyscall(); exitErr != nil {
        return exitErr 
    }
    
    // 继续 CPU 密集计算
    return nil
}
```

### 2. 看门狗与僵尸防御 (Watchdog & Zombie Defense)
调度器内部运行着一个高频看门狗。如果一个任务既不 `Yield()` 也不 `EnterSyscall()`，长期霸占槽位（例如死循环），Watchdog 会：
1. **线性注入能量惩罚**：使其在下次让权时优先级暴跌。
2. **僵尸斩首 (Zombie Mark)**：若耗时超过 `ZombieTimeout`，直接将其标记为 Zombie，强行剥夺其持有的并发槽位，防止系统 Livelock。

### 3. Context 逃生舱契约 (The Context Contract)
⚠️ **极其重要**：`future.Get(ctx)` 传入的 `ctx` **仅仅控制调用方的阻塞行为**，它 **不会** 取消调度器中实际运行的 Goroutine。
要实现真正的协作式取消，你必须在闭包内部检查 `scheduler.Context`：

```go
scheduler.SubmitVoid(gate, 10, func(ctx scheduler.Context) error {
    for {
        select {
        case <-ctx.Done(): // 监听 Gatekeeper 销毁或外部取消
            return ctx.Err()
        default:
            // do heavy work
        }
    }
})
```

---

## 调优与配置 (Tuning Config)

通过 `scheduler.Config`，你可以掌控这个宇宙的物理法则：

| 参数 | 默认值 | 描述 |
|------|--------|------|
| `InitialLimit` | `64` | 系统冷启动时的并发额度。 |
| `Min/MaxConcurrency` | `4` / `1024` | AIMD 呼吸的物理极限。Max 建议设为 `CPU核心数 * 经验系数`。 |
| `TargetLatency` | `5ms` | 系统的**时间心跳** ($\tau$)，也是 AIMD 的 P99 容忍阈值。 |
| `AIMDAlpha / Beta` | `1.0` / `0.8` | 加法增长步长与乘法衰减系数。 |
| `ZombieTimeout` | `30s` | 死锁任务被强行剥夺并发槽位的超时时间。 |
| `DeathRattleTimeout` | `5s` | `gate.Wait()` 关闭时，允许存量任务执行的最后“濒死喘息”时间。超出则强行孤立，防止微服务无法下线。 |
| `StrictLivelockPanic`| `false` | 极度严苛模式。当检测到长期零吞吐活锁时，直接 `panic` 崩溃进程。 |
| `OnError` / `OnPanic`| `nil` | 全局异常捕获回调。Gatekeeper 拒绝吞噬（Swallow）任何 Panic，所有错误都会带上 StackTrace 涌入此回调。 |

---

## 遥测与可观测性 (Metrics)

调度器提供原子级别、无锁的 $O(1)$ 快照，你可以极其安全地将其接入 Prometheus 等监控系统，不会引发观察者效应（Observer Effect）：

```go
metrics := gate.Metrics()
fmt.Printf("执行中: %d, 排队中: %d, 动态上限: %d, 僵尸泄漏: %d\n", 
    metrics.Active, metrics.Queued, metrics.Limit, metrics.Zombies)
```
