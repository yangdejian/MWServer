package main

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/arsgo/lib4go/concurrent"
	"github.com/arsgo/lib4go/logger"
	"github.com/arsgo/lib4go/utility"
)

const (
	p_mwServerRoot  = "@domain/servers"
	p_monitorConfig = "@domain/monitoring"
	p_warningConfig = "@domain/config/warning"

	p_varConfig = "@domain/var/@type/@name"

	p_rcServerTaskConfig = "@domain/monitor/@system"

	p_rcServerMWClusterClientBase = "@domain/servers/mw_"

	p_rcServerClusterServerPathFormat = "@domain/servers/@name"
)

//MWClusterClient 集群客户端
type MWClusterClient struct {
	domain     string
	domainPath string
	//appTaskRoot         string
	//appServerTaskPath   string

	mwServerRoot      string
	monitorConfigPath string
	warningConfigPath string

	rcServerConfig      string
	jobConfigPath       string
	spConfigPath        string
	rpcPublishPath      string
	rpcProviderRootPath string
	spServerTaskPath    string
	closeChans          *concurrent.ConcurrentMap
	//lastRPCServices     RPCServices
	publishLock sync.Mutex
	configCache *concurrent.ConcurrentMap
	handler     IClusterHandler
	Log         logger.ILogger
	timeout     time.Duration
	dataMap     utility.DataMap
	IP          string
}

//IClusterHandler 集群管理处理程序，用于处理与集群管理器如(zk,etcd)等之前的通信
type IClusterHandler interface {
	Exists(path ...string) (string, bool)
	CreateNode(path string, data string) error
	CreateSeqNode(path string, data string) (string, error)
	CreateTmpNode(path string, data string) (string, error)
	GetValue(path string) (string, error)
	UpdateValue(path string, value string) error
	GetChildren(path string) ([]string, error)
	WatchValue(path string, data chan string) error
	WatchChildren(path string, data chan []string) error
	WaitForConnected() bool
	WaitForDisconnected() bool
	Delete(path string) error
	Reconnect() error
	Close()
}

//NewMWClusterClient 包了一层
func NewMWClusterClient(domain string, ip string, handler IClusterHandler, loggerName string) (client *MWClusterClient, err error) {
	client = &MWClusterClient{configCache: concurrent.NewConcurrentMap()}
	client.domain = "/" + strings.TrimLeft(strings.Replace(domain, ".", "/", -1), "/")
	client.domainPath = "@" + strings.Replace(strings.TrimLeft(client.domain, "/"), "/", ".", -1)
	client.IP = ip
	client.dataMap = utility.NewDataMap()
	client.dataMap.Set("domain", client.domain)
	client.dataMap.Set("ip", client.IP)
	client.closeChans = concurrent.NewConcurrentMap()

	client.mwServerRoot = client.dataMap.Translate(p_mwServerRoot)
	client.monitorConfigPath = client.dataMap.Translate(p_monitorConfig)
	client.warningConfigPath = client.dataMap.Translate(p_warningConfig)

	//client.appServerTaskPath = client.dataMap.Translate(p_appTaskConfig)
	//client.appTaskRoot = client.dataMap.Translate(p_appTaskRoot)
	//client.rcServerConfig = client.dataMap.Translate(p_rcServerTaskConfig)
	//client.spConfigPath = client.dataMap.Translate(p_spTaskConfig)
	//client.rpcPublishPath = client.dataMap.Translate(p_servicePublishPath)
	//client.rpcProviderRootPath = client.dataMap.Translate(p_serviceProviderRoot)
	//client.jobConfigPath = client.dataMap.Translate(p_jobTaskConfig)
	//client.spServerTaskPath = client.dataMap.Translate(p_spTaskConfig)
	client.Log, err = logger.Get(loggerName)
	client.timeout = time.Hour * 10000
	client.handler = handler
	return
}
func (client *MWClusterClient) makeCloseChan() chan int {
	closeChan := make(chan int, 1)
	client.closeChans.Set(utility.GetGUID(), closeChan)
	return closeChan
}

//WaitClusterPathExists  等待集群中的指定配置出现,不存在时持续等待
func (client *MWClusterClient) WaitClusterPathExists(path string, timeout time.Duration, callback func(p string, exists bool)) {
	if p, ok := client.handler.Exists(path); ok {
		callback(p, true)
		return
	}
	callback("", false)
	timePiker := time.NewTicker(time.Second * 2)
	timeoutPiker := time.NewTicker(timeout)
	closeChan := client.makeCloseChan()
	exists := false
	npath := ""
CHECKER:
	for {
		select {
		case <-timeoutPiker.C:
			break
		case <-timePiker.C:
			if v, ok := client.handler.Exists(path); ok {
				exists = true
				npath = v
				break CHECKER
			}
		case <-closeChan:
			break CHECKER
		}
	}
	callback(npath, exists)
}

//WatchClusterValueChange 等待集群指定路径的值的变化
func (client *MWClusterClient) WatchClusterValueChange(path string, callback func()) {
	changes := make(chan string, 10)
	go func() {
		defer client.recover()
		client.handler.WatchValue(path, changes)
	}()
	closeChan := client.makeCloseChan()
	go func() {
	START:
		for {
			select {
			case <-changes:
				callback()
			case <-closeChan:
				break START
			}
		}
	}()

}

//WatchValueChangedAndNotifyNow 监控RC Config变化
func (client *MWClusterClient) WatchValueChangedAndNotifyNow(path string, callback func(string, error)) {
	client.WaitClusterPathExists(path, client.timeout, func(path string, exists bool) {
		if !exists {
			client.Log.Errorf("%s未配置或不存在", path)
		} else {
			go func() {
				defer client.recover()
				callback(client.handler.GetValue(path))
			}()
		}
	})
	client.Log.Infof("::监控:%s的变化", path)
	client.WatchClusterValueChange(path, func() {
		client.Log.Infof(" -> config:%s 值发生变化", path)
		go func() {
			defer client.recover()
			callback(client.handler.GetValue(path))
		}()
	})
}

//WatchClusterChildrenChange 监控集群指定路径的子节点变化
func (client *MWClusterClient) WatchClusterChildrenChange(path string, callback func()) {
	changes := make(chan []string, 10)
	go func() {
		defer client.recover()
		client.handler.WatchChildren(path, changes)
	}()
	closeChan := client.makeCloseChan()
	go func() {
	START:
		for {
			select {
			case <-changes:
				callback()
			case <-closeChan:
				break START
			}
		}
	}()
}

//WatchChildrenChangedThenNotifyNow 监控RC服务器变化,变化后回调指定函数
func (client *MWClusterClient) WatchChildrenChangedAndNotifyNow(path string, callback func([]string, error)) {
	client.WaitClusterPathExists(path, client.timeout, func(path string, exists bool) {
		if !exists {
			client.Log.Errorf("node:%s未配置或不存在", path)
		} else {
			go func() {
				defer client.recover()
				callback(client.handler.GetChildren(path))
			}()
		}
	})
	client.Log.Infof("::监控node>chanildren:%s的变化", path)
	client.WatchClusterChildrenChange(path, func() {
		client.Log.Infof(" -> node:%s 节点发生变化", path)
		go func() {
			defer client.recover()
			callback(client.handler.GetChildren(path))
		}()
	})
}

//WaitForConnected 监控是否已链接到当前服务器
func (client *MWClusterClient) WaitForConnected() bool {
	return client.handler.WaitForConnected()
}

//WaitForDisconnected 监控是否已链接到当前服务器
func (client *MWClusterClient) WaitForDisconnected() bool {
	return client.handler.WaitForDisconnected()
}

//Reconnect 重新连接到服务器
func (client *MWClusterClient) Reconnect() error {
	return client.handler.Reconnect()
}

//Close 关闭当前集群客户端
func (client *MWClusterClient) Close() {
	all := client.closeChans.GetAll()
	for _, v := range all {
		ch := v.(chan int)
		ch <- 1
	}
	client.handler.Close()
}

//recover 从异常中恢复
func (client *MWClusterClient) recover() {
	if r := recover(); r != nil { // recover 是哪里的？
		client.Log.Fatal(r, debug.Stack())
	}
}

//WatchServerChange 监控RC服务器变化,变化后回调指定函数
func (client *MWClusterClient) WatchServerChange(callback func([]*MWServerItem, error)) {

	client.WaitClusterPathExists(client.mwServerRoot, client.timeout, func(path string, exists bool) {
		if !exists {
			client.Log.Errorf("rc servers:%s未配置或不存在", client.mwServerRoot)
		} else {
			go func() {
				defer client.recover()
				callback(client.GetAllRCServers())
			}()
		}
	})
	client.Log.Infof("::监控rc servers:%s的变化", client.mwServerRoot)
	client.WatchClusterChildrenChange(client.mwServerRoot, func() {
		client.Log.Infof(" -> rc servers:%s 值发生变化", client.mwServerRoot)
		go func() {
			defer client.recover()
			callback(client.GetAllRCServers())
		}()
	})
}

//GetAllRCServers 获取所有RC服务器信息
func (client *MWClusterClient) GetAllRCServers() (servers []*MWServerItem, err error) {
	rcs, err := client.handler.GetChildren(client.mwServerRoot)

	fmt.Println("client.mwServerRoot：", client.mwServerRoot)
	fmt.Println("rcs：", rcs)

	if err != nil {
		fmt.Println(" -> 获取所有rc servers 出错:", client.mwServerRoot, err)

		client.Log.Errorf(" -> 获取所有rc servers 出错:%s,%v", client.mwServerRoot, err)
		return
	}
	servers = []*MWServerItem{}
	for _, v := range rcs {
		rcPath := fmt.Sprintf("%s/%s", client.mwServerRoot, v)
		config, err := client.GetRCServerValue(rcPath)
		if err != nil {

			fmt.Printf(" -> 获取rc server数据有误:%v", err)

			client.Log.Errorf(" -> 获取rc server数据有误:%v", err)
			continue
		}
		if len(config.Address) > 0 {
			servers = append(servers, config)
		}
	}
	return
}

//GetRCServerValue 获取RC服务器信息
func (client *MWClusterClient) GetRCServerValue(path string) (value *MWServerItem, err error) {
	content, err := client.handler.GetValue(path)

	fmt.Println("Path:", path)
	fmt.Println("Content:", content)

	if err != nil {
		client.Log.Errorf(" -> rc server:%s 获取server数据有误", path)
		return nil, err
	}
	value = &MWServerItem{}
	err = json.Unmarshal([]byte(content), &value)
	if err != nil {
		client.Log.Errorf(" -> rc server:%s json格式有误", content)
		return nil, err
	}
	value.Path = path

	fmt.Println("*****value:", value)

	return value, nil
}

//CreateNode 获取RC服务器信息
func (client *MWClusterClient) CreateNode(path string, value string) (p string, err error) {
	p, err = client.handler.CreateTmpNode(path, value)
	return
}

//UpdateNode 获取RC服务器信息
func (client *MWClusterClient) UpdateNode(path string, value string) (err error) {
	err = client.handler.UpdateValue(path, value)
	return
}

//CloseNode 获取RC服务器信息
func (client *MWClusterClient) CloseNode(path string) (err error) {
	return client.handler.Delete(path)
}

//SetNode 获取RC服务器信息
func (client *MWClusterClient) SetNode(path string, value string) (err error) {
	fmt.Println("SetNode:", path, value)

	if _, ok := client.handler.Exists(path); !ok {
		_, err = client.CreateNode(path, value)
	} else {
		err = client.UpdateNode(path, value)
	}

	fmt.Println("SetNode Is Error?", err)
	return
}

//CreateMWServer 创建RCServer
func (client *MWClusterClient) CreateMWServer(value string) (string, error) {
	return client.handler.CreateSeqNode(client.dataMap.Translate(p_rcServerMWClusterClientBase), value)
}

func (client *MWClusterClient) GetSourceConfig(typeName string, name string) (config string, err error) {

	dataMap := client.dataMap.Copy()
	dataMap.Set("type", typeName)
	dataMap.Set("name", name)
	path := dataMap.Translate(p_varConfig)
	cfg := client.configCache.Get(path)
	if cfg != nil {
		config = cfg.(string)
		return
	}
	values, err := client.handler.GetValue(path)
	if err != nil {
		client.Log.Errorf(" -> var config:%s 获取数据有误", path)
		return
	}
	config = string(values)
	client.configCache.Set(path, config)
	client.WatchClusterValueChange(path, func() {
		values, err := client.handler.GetValue(path)
		if err != nil {
			client.Log.Errorf(" -> var config:%s 获取数据有误", path)
			return
		}
		client.configCache.Set(path, string(values))
	})
	return
}

func (client *MWClusterClient) GetMQConfig(name string) (string, error) {
	return client.GetSourceConfig("mq", name)
}
func (client *MWClusterClient) GetElasticConfig(name string) (string, error) {
	return client.GetSourceConfig("elastic", name)
}
func (client *MWClusterClient) GetDBConfig(name string) (string, error) {
	return client.GetSourceConfig("db", name)
}
func (client *MWClusterClient) GetSMSConfig(name string) (string, error) {
	return client.GetSourceConfig("sms", name)
}
