package overview

import (
	"github.com/coroot/coroot/auditor"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/dustin/go-humanize"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
)

type View struct {
	Applications []*Application `json:"applications"`
	Nodes        *model.Table   `json:"nodes"`
}

type Application struct {
	Id         model.ApplicationId `json:"id"`
	Category   string              `json:"category"`
	Labels     model.Labels        `json:"labels"`
	Status     model.Status        `json:"status"`
	Indicators []model.Indicator   `json:"indicators"`

	Upstreams   []Link `json:"upstreams"`
	Downstreams []Link `json:"downstreams"`
}

type Link struct {
	Id        model.ApplicationId `json:"id"`
	Status    model.Status        `json:"status"`
	Direction string              `json:"direction"`
}

func Render(w *model.World) *View {
	var apps []*Application
	used := map[model.ApplicationId]bool{}
	auditor.Audit(w)
	for _, a := range w.Applications {
		app := Application{
			Id:          a.Id,
			Category:    category(a),
			Labels:      a.Labels(),
			Status:      a.Status,
			Indicators:  model.CalcIndicators(a),
			Upstreams:   []Link{},
			Downstreams: []Link{},
		}

		upstreams := map[model.ApplicationId]model.Status{}
		downstreams := map[model.ApplicationId]bool{}
		for _, i := range a.Instances {
			for _, u := range i.Upstreams {
				if u.Obsolete() || u.RemoteInstance == nil || u.RemoteInstance.OwnerId == app.Id {
					continue
				}
				status := u.Status()
				if status >= upstreams[u.RemoteInstance.OwnerId] {
					upstreams[u.RemoteInstance.OwnerId] = status
				}
			}
			for _, d := range i.Downstreams {
				if d.Obsolete() || d.Instance == nil || d.Instance.OwnerId == app.Id {
					continue
				}
				downstreams[d.Instance.OwnerId] = true
			}
		}
		for id, status := range upstreams {
			app.Upstreams = append(app.Upstreams, Link{Id: id, Status: status})
			used[a.Id] = true
			used[id] = true
		}
		for id := range downstreams {
			app.Downstreams = append(app.Downstreams, Link{Id: id})
			used[a.Id] = true
			used[id] = true
		}

		apps = append(apps, &app)
	}
	var appsUsed []*Application
	for _, a := range apps {
		if !used[a.Id] {
			continue
		}
		appsUsed = append(appsUsed, a)
	}

	table := &model.Table{Header: []string{"Node", "Status", "Availability zone", "IP", "CPU", "Memory", "Network"}}
	for _, n := range w.Nodes {
		node := model.NewTableCell(n.Name.Value()).SetLink("node")
		ips := utils.NewStringSet()

		cpuPercent, memoryPercent := model.NewTableCell(), model.NewTableCell("")

		if t := n.InstanceType.Value(); t != "" {
			node.AddTag("Type: " + t)
		}
		if l := timeseries.Last(n.CpuCapacity); !math.IsNaN(l) {
			node.AddTag("vCPU: " + strconv.Itoa(int(l)))
		}
		if l := timeseries.Last(n.CpuUsagePercent); !math.IsNaN(l) {
			cpuPercent.SetProgress(int(l), "blue")
		}

		if total := timeseries.Last(n.MemoryTotalBytes); !math.IsNaN(total) {
			node.AddTag("memory: " + humanize.Bytes(uint64(total)))
			if avail := timeseries.Last(n.MemoryAvailableBytes); !math.IsNaN(avail) {
				memoryPercent.SetProgress(int(100-avail/total*100), "deep-purple")
			}
		}

		status := model.NewTableCell().SetStatus(model.OK, "up")
		if !n.IsUp() {
			status.SetStatus(model.WARNING, "down (no metrics)")
		}

		network := model.NewTableCell()
		for _, iface := range n.NetInterfaces {
			if timeseries.Last(iface.Up) != 1 {
				continue
			}
			if timeseries.IsEmpty(iface.RxBytes) || timeseries.IsEmpty(iface.TxBytes) {
				continue
			}
			for _, ip := range iface.Addresses {
				ips.Add(ip)
			}
			if ips.Len() == 0 {
				for _, instance := range n.Instances {
					for l := range instance.TcpListens {
						if ip := net.ParseIP(l.IP); ip != nil && !ip.IsLoopback() {
							ips.Add(l.IP)
						}
					}
				}
			}
			network.NetInterfaces = append(network.NetInterfaces, model.NetInterface{
				Name: iface.Name,
				Rx:   utils.HumanBits(timeseries.Last(iface.RxBytes) * 8),
				Tx:   utils.HumanBits(timeseries.Last(iface.TxBytes) * 8),
			})
		}
		sort.Slice(network.NetInterfaces, func(i, j int) bool {
			return network.NetInterfaces[i].Name < network.NetInterfaces[j].Name
		})

		table.AddRow(
			node,
			status,
			model.NewTableCell(n.AvailabilityZone.Value()).SetUnit("("+strings.ToLower(n.CloudProvider.Value())+")"),
			model.NewTableCell(ips.Items()...),
			cpuPercent,
			memoryPercent,
			network,
		)
	}
	return &View{Applications: appsUsed, Nodes: table}
}

func category(app *model.Application) string {
	if app.IsControlPlane() {
		return "control-plane"
	}
	if app.IsMonitoring() {
		return "monitoring"
	}
	return "application"
}
