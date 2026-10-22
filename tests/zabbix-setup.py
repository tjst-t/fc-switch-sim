#!/usr/bin/env python3
"""冗長構成（fctape-ha）の監視を Zabbix 7.0 に入れる（Zabbix の API で。何度流してもよい）。

テンプレート：
  FCSIM Tape Library SNMP  … SNMP v2c で FCSIM-TAPE-LIBRARY-MIB を読む（状態・ドライブ・TapeAlert をドライブの発見で）
  FCSIM Tape Library REST  … libmgr の REST（/api/v1/library）を HTTP エージェントで読み、JSONPath で取り出す
  FCSIM FC Switch REST     … fcsw の REST（/api/v1/ports・/nameserver）を HTTP エージェントで読む（ポートの発見）
ホスト：lib1・lib2（SNMP と REST）、fab-a・fab-b（REST）

  zabbix-setup.py [--url http://172.30.31.42:8080/api_jsonrpc.php]
"""
import argparse
import json
import sys
import urllib.request

E = ".1.3.6.1.4.1.99999.1"


class Zbx:
    def __init__(self, url, user="Admin", password="zabbix"):
        self.url, self.auth, self.n = url, None, 0
        self.auth = self.call("user.login", {"username": user, "password": password})

    def call(self, method, params):
        self.n += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params, "id": self.n}
        headers = {"Content-Type": "application/json-rpc"}
        if self.auth:
            headers["Authorization"] = "Bearer " + self.auth
        req = urllib.request.Request(self.url, json.dumps(body).encode(), headers)
        with urllib.request.urlopen(req, timeout=30) as r:
            res = json.load(r)
        if "error" in res:
            raise RuntimeError(f"{method}: {res['error']}")
        return res["result"]


# ---- テンプレートの中身 ----

def snmp_template():
    items = [
        dict(name="Library serial", key_="fcsim.lib.serial", snmp_oid=f"{E}.1.3.0", value_type=1, delay="1m"),
        dict(name="Library status", key_="fcsim.lib.status", snmp_oid=f"{E}.1.4.0", value_type=3, delay="10s",
             valuemap="FCSIM library status"),
        dict(name="Drives with TapeAlert", key_="fcsim.lib.alertdrives", snmp_oid=f"{E}.1.8.0", value_type=3, delay="10s"),
        dict(name="Drive count", key_="fcsim.lib.drivecount", snmp_oid=f"{E}.1.5.0", value_type=3, delay="1m"),
    ]
    triggers = [
        dict(description="Tape library {HOST.NAME} is not online (SNMP)",
             expression="last(/FCSIM Tape Library SNMP/fcsim.lib.status)<>1", priority=4),
        dict(description="Tape library {HOST.NAME}: SNMP not responding",
             expression="nodata(/FCSIM Tape Library SNMP/fcsim.lib.status,1m)=1", priority=3),
    ]
    rule = dict(
        name="Drives", key_="fcsim.drives.discovery", delay="1m", type=20,
        snmp_oid=f"discovery[{{#SERIAL}},{E}.2.1.2,{{#PRODUCT}},{E}.2.1.3]",
        item_prototypes=[
            dict(name="Drive {#SNMPINDEX} ({#SERIAL}) state", key_="fcsim.drive.state[{#SNMPINDEX}]", type=20,
                 snmp_oid=f"{E}.2.1.4.{{#SNMPINDEX}}", value_type=3, delay="10s", valuemap="FCSIM drive state"),
            dict(name="Drive {#SNMPINDEX} ({#SERIAL}) volume", key_="fcsim.drive.volume[{#SNMPINDEX}]", type=20,
                 snmp_oid=f"{E}.2.1.5.{{#SNMPINDEX}}", value_type=1, delay="10s"),
            dict(name="Drive {#SNMPINDEX} ({#SERIAL}) TapeAlert", key_="fcsim.drive.alerts[{#SNMPINDEX}]", type=20,
                 snmp_oid=f"{E}.2.1.6.{{#SNMPINDEX}}", value_type=1, delay="10s"),
            dict(name="Drive {#SNMPINDEX} ({#SERIAL}) loads", key_="fcsim.drive.loads[{#SNMPINDEX}]", type=20,
                 snmp_oid=f"{E}.2.1.7.{{#SNMPINDEX}}", value_type=3, delay="30s"),
        ],
        trigger_prototypes=[
            dict(description="Drive {#SNMPINDEX} ({#SERIAL}) on {HOST.NAME}: TapeAlert {ITEM.LASTVALUE}",
                 expression="length(last(/FCSIM Tape Library SNMP/fcsim.drive.alerts[{#SNMPINDEX}]))>0", priority=3),
        ],
    )
    return items, triggers, [rule]


def lib_rest_template():
    master = dict(name="libmgr library (raw)", key_="fcsim.rest.library", type=19, url="http://{HOST.IP}:8080/api/v1/library",
                  value_type=4, delay="10s", history="1h", trends="0")
    deps = [
        dict(name="Library status (REST)", key_="fcsim.rest.status", path="$.library.status", value_type=1),
        dict(name="Loaded drives (REST)", key_="fcsim.rest.loaded", path="$.drives[?(@.state == 'loaded')].length()", value_type=3),
        dict(name="Full I/E slots (REST)", key_="fcsim.rest.iefull",
             path=None, value_type=3,
             js="return JSON.parse(value).slots.filter(function (s) { return s.type === 'importExport' && s.full; }).length;"),
    ]
    triggers = [
        dict(description="Tape library {HOST.NAME} is not online (REST)",
             expression='last(/FCSIM Tape Library REST/fcsim.rest.status)<>"online"', priority=4),
        dict(description="Tape library {HOST.NAME}: cartridge waiting in I/E slot",
             expression="last(/FCSIM Tape Library REST/fcsim.rest.iefull)>0", priority=1),
    ]
    return master, deps, triggers


def switch_template():
    ports = dict(name="fcsw ports (raw)", key_="fcsim.sw.ports", type=19, url="http://{HOST.IP}:8080/api/v1/ports",
                 value_type=4, delay="10s", history="1h", trends="0")
    ns = dict(name="fcsw nameserver (raw)", key_="fcsim.sw.ns", type=19, url="http://{HOST.IP}:8080/api/v1/nameserver",
              value_type=4, delay="10s", history="1h", trends="0")
    login_count = dict(name="Logged-in N_Ports", key_="fcsim.sw.logins", master="fcsim.sw.ns", path="$.length()", value_type=3)
    rule = dict(
        name="FC ports", key_="fcsim.sw.ports.discovery", type=18, master="fcsim.sw.ports", delay="0",
        lld_macro_paths=[{"lld_macro": "{#PORT}", "path": "$.index"}, {"lld_macro": "{#IF}", "path": "$.interface"}],
        item_prototypes=[
            dict(name="Port {#PORT} ({#IF}) state", key_="fcsim.sw.port.state[{#PORT}]", master="fcsim.sw.ports",
                 path="$[?(@.index == {#PORT})].state.first()", value_type=1),
            dict(name="Port {#PORT} ({#IF}) zone drops", key_="fcsim.sw.port.zonedrops[{#PORT}]", master="fcsim.sw.ports",
                 path="$[?(@.index == {#PORT})].counters.zoneDrops.first()", value_type=3),
        ],
        trigger_prototypes=[
            dict(description="FC switch {HOST.NAME} port {#PORT} ({#IF}) is {ITEM.LASTVALUE}",
                 expression='last(/FCSIM FC Switch REST/fcsim.sw.port.state[{#PORT}])<>"Online"', priority=3),
        ],
    )
    triggers = [
        dict(description="FC switch {HOST.NAME}: REST not responding",
             expression="nodata(/FCSIM FC Switch REST/fcsim.sw.logins,1m)=1", priority=4),
    ]
    return [ports, ns], [login_count], [rule], triggers


T = "FCSIM Tape Library Traps"
# Zabbix の SNMP トラップの項目は、トラップの本文（snmptraps のコンテナが書く。MIB を入れていないので OID は数字）を正規表現で選ぶ。
# 1 つのトラップは、正規表現に合う項目の全部に入る。[\s\S] は改行も含めた任意の 1 文字。
TRAP_STATUS = r"99999\.1\.0\.1\s"
TRAP_ALERT = r"99999\.1\.0\.4\s"


def trap_template():
    drv = "{#SNMPINDEX}"
    BS = "\\"   # バックスラッシュ 1 文字
    # 項目キーの引用の中では \ はそのまま（正規表現の \. になる）。式の文字列の中では \ と " を \\ と \" で書く
    key = 'snmptrap["99999' + BS + '.1' + BS + '.2' + BS + '.1' + BS + '.1' + BS + '.' + drv + ' = "]'
    def zstr(regex):  # 正規表現を、トリガーの式の文字列にする
        return '"' + regex.replace(BS, BS + BS).replace('"', BS + '"') + '"'
    STATUS_IS = "99999" + BS + ".1" + BS + ".1" + BS + ".4" + BS + ".0 = "
    items = [
        dict(name="Trap: library status change", key_=f'snmptrap["{TRAP_STATUS}"]'),
        dict(name="Trap: drive alert (any drive)", key_=f'snmptrap["{TRAP_ALERT}"]'),
        dict(name="Trap: drive load/unload, I/E slot", key_=r'snmptrap["99999\.1\.0\.[2356]\s"]'),
        dict(name="Trap: unmatched", key_="snmptrap.fallback"),
    ]
    # 状態：offline(2) のトラップで障害、online(1) のトラップで解消（find の期間を省くと最後の値だけを見る）
    triggers = [
        dict(description="Tape library {HOST.NAME} went offline (SNMP trap)", priority=4,
             expression=f'find(/{T}/snmptrap["{TRAP_STATUS}"],,"regexp",{zstr(STATUS_IS + "2")})=1',
             recovery_mode=1,
             recovery_expression=f'find(/{T}/snmptrap["{TRAP_STATUS}"],,"regexp",{zstr(STATUS_IS + "1")})=1'),
        dict(description="Tape library {HOST.NAME}: unmatched SNMP trap received", priority=1,
             expression=f"count(/{T}/snmptrap.fallback,10m)>0"),
    ]
    # ドライブごと：SNMP でドライブを発見し、そのドライブの番号が入ったトラップを受ける
    alert_on = zstr('2' + BS + '.1' + BS + '.6' + BS + '.' + drv + ' = "[0-9]')
    alert_off = zstr('2' + BS + '.1' + BS + '.6' + BS + '.' + drv + ' = ""')
    rule = dict(
        name="Drives (traps)", key_="fcsim.trapdrives.discovery", delay="1m", type=20,
        snmp_oid=f"discovery[{{#SERIAL}},{E}.2.1.2]",
        item_prototypes=[dict(name="Trap: drive {#SNMPINDEX} ({#SERIAL}) TapeAlert", key_=key, type=17, value_type=2)],
        trigger_prototypes=[dict(
            description="Drive {#SNMPINDEX} ({#SERIAL}) on {HOST.NAME}: TapeAlert raised (SNMP trap)", priority=3,
            expression=f'find(/{T}/{key},,"regexp",{alert_on})=1',
            recovery_mode=1,
            recovery_expression=f'find(/{T}/{key},,"regexp",{alert_off})=1')],
    )
    return items, triggers, rule


SW = ".1.3.6.1.4.1.99999.2"
TS = "FCSIM FC Switch SNMP"
TT = "FCSIM FC Switch Traps"
B = "\\"  # バックスラッシュ 1 文字


def rx(dotted):  # "99999.2.0.1" → 正規表現 99999\.2\.0\.1
    return dotted.replace(".", B + ".")


def zq(regex):  # 正規表現を、トリガーの式の文字列にする
    return '"' + regex.replace(B, B + B).replace('"', B + '"') + '"'


def switch_snmp_template():
    items = [
        dict(name="Logged-in N_Ports (SNMP)", key_="fcsim.sw.snmp.logins", snmp_oid=f"{SW}.1.5.0", value_type=3, delay="10s"),
        dict(name="Zones (SNMP)", key_="fcsim.sw.snmp.zones", snmp_oid=f"{SW}.1.6.0", value_type=3, delay="30s"),
        dict(name="Zoning changes (SNMP)", key_="fcsim.sw.snmp.zoningchanges", snmp_oid=f"{SW}.1.7.0", value_type=3, delay="30s"),
    ]
    triggers = [dict(description="FC switch {HOST.NAME}: SNMP not responding", priority=4,
                     expression=f"nodata(/{TS}/fcsim.sw.snmp.logins,1m)=1")]
    rule = dict(
        name="FC ports (SNMP)", key_="fcsim.sw.snmp.ports", delay="1m", type=20,
        snmp_oid=f"discovery[{{#IF}},{SW}.2.1.2,{{#PORTNUM}},{SW}.2.1.1]",
        item_prototypes=[
            dict(name="Port {#PORTNUM} ({#IF}) state (SNMP)", key_="fcsim.sw.snmp.state[{#SNMPINDEX}]", type=20,
                 snmp_oid=f"{SW}.2.1.4.{{#SNMPINDEX}}", value_type=3, delay="10s", valuemap="FCSIM port state"),
            dict(name="Port {#PORTNUM} ({#IF}) zone drops (SNMP)", key_="fcsim.sw.snmp.zonedrops[{#SNMPINDEX}]", type=20,
                 snmp_oid=f"{SW}.2.1.7.{{#SNMPINDEX}}", value_type=3, delay="30s"),
        ],
        trigger_prototypes=[dict(description="FC switch {HOST.NAME} port {#PORTNUM} ({#IF}) not online (SNMP)", priority=2,
                                 expression=f"last(/{TS}/fcsim.sw.snmp.state[{{#SNMPINDEX}}])<>1")],
    )
    return items, triggers, rule


def switch_trap_template():
    k_state = f'snmptrap["{rx("99999.2.0.1")}{B}s"]'
    k_login = f'snmptrap["{rx("99999.2.0.")}[23]{B}s"]'
    k_zone = f'snmptrap["{rx("99999.2.0.4")}{B}s"]'
    items = [
        dict(name="Trap: port state change", key_=k_state),
        dict(name="Trap: login/logout", key_=k_login),
        dict(name="Trap: zoning change", key_=k_zone),
        dict(name="Trap: unmatched", key_="snmptrap.fallback"),
    ]
    triggers = [
        # ゾーンの変更は「お知らせ」：10 分間だけ出る
        dict(description="FC switch {HOST.NAME}: zoning changed (SNMP trap)", priority=1,
             expression=f"count(/{TT}/{k_zone},10m)>0"),
        dict(description="FC switch {HOST.NAME}: unmatched SNMP trap received", priority=1,
             expression=f"count(/{TT}/snmptrap.fallback,10m)>0"),
    ]
    i = "{#SNMPINDEX}"
    k_port = f'snmptrap["{rx("99999.2.2.1.1.")}{i} = "]'   # そのポートの portStateChange（fswPortNumber の変数を含む）
    state_is = rx("2.2.1.4.") + i + " = "
    rule = dict(
        name="FC ports (traps)", key_="fcsim.sw.trapports", delay="1m", type=20,
        snmp_oid=f"discovery[{{#IF}},{SW}.2.1.2,{{#PORTNUM}},{SW}.2.1.1]",
        item_prototypes=[dict(name="Trap: port {#PORTNUM} ({#IF}) state change", key_=k_port, type=17, value_type=2)],
        trigger_prototypes=[dict(
            description="FC switch {HOST.NAME} port {#PORTNUM} ({#IF}) went down (SNMP trap)", priority=3,
            expression=f"find(/{TT}/{k_port},,\"regexp\",{zq(state_is + '[23]')})=1",
            recovery_mode=1,
            recovery_expression=f"find(/{TT}/{k_port},,\"regexp\",{zq(state_is + '1')})=1")],
    )
    return items, triggers, rule


def add_rule(z, tid, rule, vm=None):
    rid = z.call("discoveryrule.create", {k: v for k, v in rule.items() if not k.endswith("_prototypes")} | {"hostid": tid})["itemids"][0]
    for ip in rule["item_prototypes"]:
        p = {k: v for k, v in ip.items() if k != "valuemap"} | {"hostid": tid, "ruleid": rid}
        if "valuemap" in ip:
            p["valuemapid"] = vm[ip["valuemap"]]
        z.call("itemprototype.create", p)
    for tp in rule["trigger_prototypes"]:
        z.call("triggerprototype.create", tp)


# ---- Zabbix に入れる ----

def jsonpath_pre(path):
    return [{"type": 12, "params": path, "error_handler": 0, "error_handler_params": ""}]


def ensure_group(z, name):
    g = z.call("hostgroup.get", {"filter": {"name": [name]}})
    return g[0]["groupid"] if g else z.call("hostgroup.create", {"name": name})["groupids"][0]


def ensure_template_group(z, name):
    g = z.call("templategroup.get", {"filter": {"name": [name]}})
    return g[0]["groupid"] if g else z.call("templategroup.create", {"name": name})["groupids"][0]


def ensure_template(z, name, gid):
    t = z.call("template.get", {"filter": {"host": [name]}})
    if t:
        z.call("template.delete", [t[0]["templateid"]])  # 作り直す（中身を確実に揃える）
    return z.call("template.create", {"host": name, "groups": [{"groupid": gid}]})["templateids"][0]


def ensure_valuemap(z, hostid, name, mapping):
    z.call("valuemap.create", {"hostid": hostid, "name": name,
                               "mappings": [{"value": k, "newvalue": v} for k, v in mapping.items()]})


def item_ids(z, hostid):
    return {i["key_"]: i["itemid"] for i in z.call("item.get", {"hostids": hostid, "output": ["key_"]})}


def build(z):
    gid = ensure_group(z, "FCSIM")
    tgid = ensure_template_group(z, "Templates/FCSIM")
    # SNMP
    tid = ensure_template(z, "FCSIM Tape Library SNMP", tgid)
    ensure_valuemap(z, tid, "FCSIM library status", {"1": "online", "2": "offline", "3": "unknown"})
    ensure_valuemap(z, tid, "FCSIM drive state", {"1": "empty", "2": "loaded"})
    vm = {v["name"]: v["valuemapid"] for v in z.call("valuemap.get", {"hostids": tid, "output": ["valuemapid", "name"]})}
    items, triggers, rules = snmp_template()
    for it in items:
        p = {k: v for k, v in it.items() if k != "valuemap"}
        p.update(hostid=tid, type=20)
        if "valuemap" in it:
            p["valuemapid"] = vm[it["valuemap"]]
        z.call("item.create", p)
    for r in rules:
        rid = z.call("discoveryrule.create", {k: v for k, v in r.items() if not k.endswith("_prototypes")} | {"hostid": tid})["itemids"][0]
        for ip in r["item_prototypes"]:
            p = {k: v for k, v in ip.items() if k != "valuemap"} | {"hostid": tid, "ruleid": rid}
            if "valuemap" in ip:
                p["valuemapid"] = vm[ip["valuemap"]]
            z.call("itemprototype.create", p)
        for tp in r["trigger_prototypes"]:
            z.call("triggerprototype.create", tp)
    for t in triggers:
        z.call("trigger.create", t)
    snmp_tid = tid
    # REST（ライブラリ）
    tid = ensure_template(z, "FCSIM Tape Library REST", tgid)
    master, deps, triggers = lib_rest_template()
    z.call("item.create", master | {"hostid": tid})
    mid = item_ids(z, tid)[master["key_"]]
    for d in deps:
        # Zabbix の JSONPath は true との比較を扱えず、空のときは「一致なし」で失敗するので、数えるものは JavaScript で
        pre = [{"type": 21, "params": d["js"], "error_handler": 0, "error_handler_params": ""}] if d.get("js") else jsonpath_pre(d["path"])
        z.call("item.create", {"hostid": tid, "name": d["name"], "key_": d["key_"], "type": 18, "master_itemid": mid,
                               "value_type": d["value_type"], "preprocessing": pre})
    for t in triggers:
        z.call("trigger.create", t)
    lib_rest_tid = tid
    # REST（スイッチ）
    tid = ensure_template(z, "FCSIM FC Switch REST", tgid)
    masters, deps, rules, triggers = switch_template()
    for m in masters:
        z.call("item.create", m | {"hostid": tid})
    ids = item_ids(z, tid)
    for d in deps:
        z.call("item.create", {"hostid": tid, "name": d["name"], "key_": d["key_"], "type": 18, "master_itemid": ids[d["master"]],
                               "value_type": d["value_type"], "preprocessing": jsonpath_pre(d["path"])})
    for r in rules:
        rid = z.call("discoveryrule.create", {"hostid": tid, "name": r["name"], "key_": r["key_"], "type": 18,
                                               "master_itemid": ids[r["master"]], "delay": r["delay"],
                                               "lld_macro_paths": r["lld_macro_paths"]})["itemids"][0]
        for ip in r["item_prototypes"]:
            z.call("itemprototype.create", {"hostid": tid, "ruleid": rid, "name": ip["name"], "key_": ip["key_"], "type": 18,
                                            "master_itemid": ids[ip["master"]], "value_type": ip["value_type"],
                                            "preprocessing": jsonpath_pre(ip["path"])})
        for tp in r["trigger_prototypes"]:
            z.call("triggerprototype.create", tp)
    for t in triggers:
        z.call("trigger.create", t)
    sw_tid = tid
    # トラップ
    tid = ensure_template(z, T, tgid)
    items, triggers, rule = trap_template()
    for it in items:
        z.call("item.create", it | {"hostid": tid, "type": 17, "value_type": 2})
    rid = z.call("discoveryrule.create", {k: v for k, v in rule.items() if not k.endswith("_prototypes")} | {"hostid": tid})["itemids"][0]
    for ip in rule["item_prototypes"]:
        z.call("itemprototype.create", ip | {"hostid": tid, "ruleid": rid})
    for tp in rule["trigger_prototypes"]:
        z.call("triggerprototype.create", tp)
    for t in triggers:
        z.call("trigger.create", t)
    trap_tid = tid
    # スイッチの SNMP
    tid = ensure_template(z, TS, tgid)
    ensure_valuemap(z, tid, "FCSIM port state", {"1": "online", "2": "noLogin", "3": "disabled"})
    svm = {v["name"]: v["valuemapid"] for v in z.call("valuemap.get", {"hostids": tid, "output": ["valuemapid", "name"]})}
    items, triggers, rule = switch_snmp_template()
    for it in items:
        z.call("item.create", it | {"hostid": tid, "type": 20})
    add_rule(z, tid, rule, svm)
    for t in triggers:
        z.call("trigger.create", t)
    sw_snmp_tid = tid
    # スイッチのトラップ
    tid = ensure_template(z, TT, tgid)
    items, triggers, rule = switch_trap_template()
    for it in items:
        z.call("item.create", it | {"hostid": tid, "type": 17, "value_type": 2})
    add_rule(z, tid, rule)
    for t in triggers:
        z.call("trigger.create", t)
    sw_trap_tid = tid
    # ホスト（トラップは送り元の IP がホストの SNMP のインタフェースと一致するもので受ける）
    hosts = [
        ("lib1", "172.30.31.22", [snmp_tid, lib_rest_tid, trap_tid], True),
        ("lib2", "172.30.31.23", [snmp_tid, lib_rest_tid, trap_tid], True),
        ("fab-a", "172.30.31.10", [sw_tid, sw_snmp_tid, sw_trap_tid], True),
        ("fab-b", "172.30.31.11", [sw_tid, sw_snmp_tid, sw_trap_tid], True),
    ]
    for name, ip, tids, snmp in hosts:
        old = z.call("host.get", {"filter": {"host": [name]}})
        if old:
            z.call("host.delete", [old[0]["hostid"]])
        iface = {"type": 2, "main": 1, "useip": 1, "ip": ip, "dns": "", "port": "161",
                 "details": {"version": 2, "community": "public"}} if snmp else \
                {"type": 1, "main": 1, "useip": 1, "ip": ip, "dns": "", "port": "10050"}
        z.call("host.create", {"host": name, "groups": [{"groupid": gid}], "interfaces": [iface],
                               "templates": [{"templateid": t} for t in tids]})
    # 既定の「Zabbix server」ホスト（エージェントが無いので障害になる）は消す
    for h in z.call("host.get", {"filter": {"host": ["Zabbix server"]}}):
        z.call("host.delete", [h["hostid"]])
    print("Zabbix に入れた：テンプレート 6、ホスト", ", ".join(h[0] for h in hosts))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://172.30.31.42:8080/api_jsonrpc.php")
    a = ap.parse_args()
    try:
        build(Zbx(a.url))
    except Exception as e:
        print("失敗：", e, file=sys.stderr)
        sys.exit(1)
