const { connect } = require('nats.ws');
connect({ servers: 'ws://47.110.255.240:8443', token: 'ymm_rpc_2026' })
  .then(async (nc) => {
    console.log('Connected!');
    const js = nc.jetstream();
    console.log('Getting KV...');
    const kv = await js.views.kv('CRUSH_SESSIONS');
    console.log('Got KV! Watching...');
    const iter = await kv.watch({ key: '*' });
    console.log('Watching started! Waiting for first value...');
    for await (const e of iter) {
      console.log('Event:', e.key, e.operation, e.value ? new TextDecoder().decode(e.value) : 'null');
    }
  })
  .catch(console.error);
