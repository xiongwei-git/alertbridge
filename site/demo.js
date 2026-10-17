'use strict';
// Presentation only: no network, credentials, persistence or backend claims.
(() => {
  let active = false;
  let received = 0;
  let delivered = 0;
  let events = [];
  const el = id => document.getElementById(id);
  const show = (id, value) => { el(id).textContent = value; };
  const update = () => {
    show('active', active ? 1 : 0);
    show('received', received);
    show('delivered', delivered);
    el('fire').disabled = active;
    el('repeat').disabled = !active;
    el('resolve').disabled = !active;
  };
  function event(status, repeated = false) {
    received++;
    active = status === 'firing';
    if (!repeated) delivered++;
    const payload = {
      event_id: `demo-${received}`, source: 'demo-server', routing_key: 'ops',
      status, severity: 'warning', title: status === 'resolved' ? '磁盘空间恢复' : '磁盘使用率超过 90%',
      message: '虚构演示事件', occurred_at: new Date().toISOString(), dedupe_key: 'demo-disk'
    };
    const result = repeated ? '模拟：重复故障被去重，活跃故障保持 1 个，不新增通知。' :
      status === 'resolved' ? '模拟：故障已恢复，新增 1 条恢复通知。' : '模拟：创建活跃故障，新增 1 条故障通知。';
    events.push({ id: received, ...payload, demo_delivery: repeated ? 'deduplicated' : 'simulated_sent' });
    show('state', result);
    show('payload', JSON.stringify(payload, null, 2));
    if (received === 1) el('timeline').replaceChildren();
    const row = document.createElement('li');
    const time = document.createElement('time');
    time.dateTime = payload.occurred_at;
    time.textContent = new Date(payload.occurred_at).toLocaleTimeString('zh-CN', {hour12: false});
    row.append(time, document.createTextNode(result));
    el('timeline').prepend(row);
    // Bound browser memory during long demo sessions.
    events = events.slice(-100);
    while (el('timeline').children.length > 30) el('timeline').lastChild.remove();
    show('agent-result', '演示数据已变化。点击查询获取最近的模拟结果。');
    update();
  }
  el('fire').addEventListener('click', () => event('firing'));
  el('repeat').addEventListener('click', () => event('firing', true));
  el('resolve').addEventListener('click', () => event('resolved'));
  el('query').addEventListener('click', () => {
    show('agent-result', JSON.stringify({ demo: true, tool: 'list_alerts', arguments: {order: 'desc', limit: 10}, events: events.slice(-10).reverse() }, null, 2));
  });
  el('reset').addEventListener('click', () => {
    active = false; received = 0; delivered = 0; events = [];
    show('state', '演示已重置。点击“发生故障”开始。');
    show('payload', '尚无请求；本页面不会调用 API。');
    show('agent-result', '参数：{"order":"desc","limit":10}\n等待查询演示数据。');
    const row = document.createElement('li'); row.className = 'empty';
    row.textContent = '这里将展示事件与模拟投递的变化。';
    el('timeline').replaceChildren(row); update();
  });
  update();
})();
