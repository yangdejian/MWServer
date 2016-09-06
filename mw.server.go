package main

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/arsgo/ars/base"
	"github.com/arsgo/ars/cluster"
	"github.com/arsgo/ars/servers/config"
	"github.com/arsgo/lib4go/concurrent"
	"github.com/arsgo/lib4go/logger"
	"github.com/arsgo/lib4go/zkclient"
)

//MWServer RC Server
type MWServer struct {
	monitorSystems        *concurrent.ConcurrentMap // 正在被监控的系统 map[string]string
	monitorConfigs        *concurrent.ConcurrentMap // 监控配置 map[string]MonitorConfig
	warningConfig         WarningConfig             // 报警配置
	sameIPWarningMessages *concurrent.ConcurrentMap // 同一个IP的报警消息(有效期内只发送一次)
	currentListenPaths    *concurrent.ConcurrentMap // 当前已监听的路径 map[string]string
	clusterClient         *MWClusterClient
	startSync             base.Sync
	clusterServers        []string
	IsMaster              bool
	Log                   logger.ILogger
	snapLogger            logger.ILogger
	snap                  MWSnap
	conf                  *config.SysConfig
	loggerName            string
	version               string
}

//NewMWServer 创建RC Server服务器
func NewMWServer(conf *config.SysConfig) (rc *MWServer, err error) {
	rc = &MWServer{loggerName: "mw.server", version: "0.1.15", conf: conf}

	rc.monitorSystems = concurrent.NewConcurrentMap()
	rc.monitorConfigs = concurrent.NewConcurrentMap()

	rc.currentListenPaths = concurrent.NewConcurrentMap()
	rc.startSync = base.NewSync(1)
	rc.Log, err = logger.Get(rc.loggerName)
	if err != nil {
		return
	}
	rc.snapLogger, err = logger.Get("mw.snap")
	if err != nil {
		return
	}

	//rc.snapLogger.Show(false)
	return
}

//init 初始化服务
func (rc *MWServer) init() (err error) {
	defer rc.recover()
	rc.Log.Infof(" -> 初始化 %s...", rc.conf.Domain)
	rc.clusterServers = rc.conf.ZKServers

	handler, err := zkClient.New(rc.conf.ZKServers, time.Second*1, rc.loggerName)
	if err != nil {
		return err
	}

	rc.clusterClient, err = NewMWClusterClient(rc.conf.Domain, rc.conf.IP, handler, rc.loggerName)
	if err != nil {
		return err
	}
	//rc.spRPCClient = rpc.NewRPCClient(rc.clusterClient, rc.loggerName)
	rc.snap = MWSnap{Domain: rc.conf.Domain, Server: cluster.SERVER_UNKNOWN, Version: rc.version, Refresh: 60, mwServer: rc}
	//rc.rcRPCHandler = proxy.NewRPCClientProxy(rc.clusterClient, rc.spRPCClient, rc.loggerName)
	//rc.rcRPCServer = server.NewRPCServer(rc.rcRPCHandler, rc.loggerName, rc.rpcServerCollector)

	return nil
}

//Start 启动服务
func (rc *MWServer) Start() (err error) {
	defer rc.recover()
	rc.Log.Info(" -> 启动 mw server...")
	if err = rc.init(); err != nil {
		rc.Log.Error(err)
		return
	}

	if !rc.clusterClient.WaitForConnected() {
		return
	}
	//启动RPC服务,供APP,SP调用
	//rc.rcRPCServer.Start()

	fmt.Println(" -> 绑定RC服务...")

	//绑定RC服务
	if err = rc.BindMWServer(); err != nil {
		fmt.Println(" -> rc.BindMWServer异常...", err)
		return
	}

	fmt.Println("rc.BindMWServer 执行结束")

	rc.startSync.Wait()
	go rc.startRefreshSnap()
	//go rc.startMonitor()
	//go rc.clearMem()
	rc.Log.Info(" -> mw server 启动完成...")
	return
}

//Stop 停止服务
func (rc *MWServer) Stop() error {
	rc.Log.Info(" -> 退出 mw server...")
	defer rc.recover()
	//rc.clusterClient.CloseMWServer(rc.snap.path)
	rc.clusterClient.Close()
	/*cross := rc.crossDomain.GetAll()
	for _, v := range cross {
		cls := v.(cluster.IClusterClient)
		cls.Close()
	}*/
	return nil
}

func (rc *MWServer) recover() (err error) {
	if r := recover(); r != nil {
		rc.Log.Fatal(r, string(debug.Stack()))
	}
	return
}
