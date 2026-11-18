// MPC audio player for MPC TOOLS pages: plays /audio/stream (Link Audio records from linkaudio-rx, passed on by
// MPC TOOLS) through Web Audio. Used by the header's Audio button and by the OBS page.
//
//   var p = new MPCAudio({ onState: function (s) {}, volume: 1 });
//   p.start(); p.stop(); p.setVolume(0.8);
//
// onState gets { playing: bool, level: 0..1, buffered: seconds, rate: Hz }, a few times a second.
(function () {
  // The worklet: a ring buffer per channel. It waits until TARGET seconds are buffered, plays, and if it
  // falls more than MAX behind (the PC was busy) it skips ahead, so the sound stays close to the MPC.
  var WORKLET = [
    'class P extends AudioWorkletProcessor {',
    '  constructor() { super(); var n = sampleRate * 4; this.L = new Float32Array(n); this.R = new Float32Array(n);',
    '    this.n = n; this.w = 0; this.r = 0; this.primed = false; this.peak = 0; this.t = 0;',
    '    this.target = Math.round(sampleRate * 0.09); this.max = Math.round(sampleRate * 0.35);',
    '    this.port.onmessage = (e) => { var d = e.data;',
    '      if (d.reset) { this.w = this.r = 0; this.primed = false; return; }',
    '      var l = d.l, r = d.r; for (var i = 0; i < l.length; i++) { this.L[this.w] = l[i]; this.R[this.w] = r[i]; this.w = (this.w + 1) % this.n; } }; }',
    '  avail() { return (this.w - this.r + this.n) % this.n; }',
    '  process(ins, outs) { var o = outs[0], L = o[0], R = o[1] || o[0], f = L.length, a = this.avail();',
    '    if (!this.primed && a >= this.target) this.primed = true;',
    '    if (this.primed && a > this.max) { this.r = (this.w - this.target + this.n) % this.n; a = this.target; }',
    '    if (this.primed && a >= f) { for (var i = 0; i < f; i++) { L[i] = this.L[this.r]; R[i] = this.R[this.r];',
    '        var m = Math.abs(L[i]) > Math.abs(R[i]) ? Math.abs(L[i]) : Math.abs(R[i]); if (m > this.peak) this.peak = m;',
    '        this.r = (this.r + 1) % this.n; } }',
    '    else { L.fill(0); if (R !== L) R.fill(0); if (this.primed) this.primed = false; }',
    '    if ((this.t += f) >= sampleRate / 8) { this.port.postMessage({ level: this.peak, buffered: a / sampleRate, playing: this.primed });',
    '      this.t = 0; this.peak = 0; }',
    '    return true; } }',
    'registerProcessor("mpc-audio", P);'
  ].join('\n');

  function MPCAudio(opts) {
    this.opts = opts || {};
    this.on = false;
    this.ctx = null; this.node = null; this.gain = null; this.rate = 0;
    this.volume = this.opts.volume == null ? 1 : this.opts.volume;
    this.abort = null; this.gen = 0;
  }

  MPCAudio.prototype.setVolume = function (v) {
    this.volume = v;
    if (this.gain) this.gain.gain.value = v;
  };

  MPCAudio.prototype.report = function (s) {
    if (this.opts.onState) this.opts.onState(s);
  };

  // A Web Audio context at the stream's own rate (no resampling), made again if the rate changes.
  MPCAudio.prototype.setup = function (rate) {
    var self = this;
    if (this.ctx && this.rate === rate) return this.ready;
    if (this.ctx) this.ctx.close();
    this.rate = rate;
    this.ctx = new AudioContext({ sampleRate: rate, latencyHint: 'interactive' });
    var url = URL.createObjectURL(new Blob([WORKLET], { type: 'text/javascript' }));
    var ctx = this.ctx;
    this.ready = ctx.audioWorklet.addModule(url).then(function () {
      if (ctx !== self.ctx) return;
      self.node = new AudioWorkletNode(ctx, 'mpc-audio', { outputChannelCount: [2] });
      self.gain = ctx.createGain();
      self.gain.gain.value = self.volume;
      self.node.connect(self.gain).connect(ctx.destination);
      self.node.port.onmessage = function (e) {
        var d = e.data;
        self.report({ playing: d.playing, level: d.level, buffered: d.buffered, rate: rate });
      };
      if (ctx.state !== 'running') ctx.resume();
    });
    return this.ready;
  };

  MPCAudio.prototype.start = function () {
    if (this.on) return;
    this.on = true;
    // Made here, inside the tap, so the browser lets it play.
    if (!this.ctx) this.setup(48000);
    else if (this.ctx.state !== 'running') this.ctx.resume();
    this.connect(++this.gen);
  };

  MPCAudio.prototype.stop = function () {
    this.on = false;
    this.gen++;
    if (this.abort) this.abort.abort();
    if (this.ctx) { this.ctx.close(); this.ctx = null; this.node = null; this.gain = null; this.rate = 0; }
    this.report({ playing: false, level: 0, buffered: 0, rate: 0 });
  };

  MPCAudio.prototype.connect = function (gen) {
    var self = this;
    if (!this.on || gen !== this.gen) return;
    var ac = new AbortController();
    this.abort = ac;
    var pending = new Uint8Array(0);

    function retry() {
      if (self.on && gen === self.gen) setTimeout(function () { self.connect(gen); }, 1500);
    }

    fetch('/audio/stream', { signal: ac.signal, cache: 'no-store' }).then(function (resp) {
      if (!resp.ok || !resp.body) throw new Error('no stream');
      var reader = resp.body.getReader();
      if (self.node) self.node.port.postMessage({ reset: true });
      function read() {
        return reader.read().then(function (res) {
          if (res.done || gen !== self.gen) return;
          var chunk = res.value;
          var buf = new Uint8Array(pending.length + chunk.length);
          buf.set(pending); buf.set(chunk, pending.length);
          var pos = 0;
          while (buf.length - pos >= 16) {
            if (buf[pos] !== 76 || buf[pos + 1] !== 65) { pos = buf.length; break; } // lost step: drop this chunk
            var dv = new DataView(buf.buffer, buf.byteOffset + pos);
            var nch = buf[pos + 2], frames = dv.getUint16(4, true), rate = dv.getUint32(8, true);
            var size = 16 + nch * frames * 2;
            if (buf.length - pos < size) break;
            self.play(dv, nch, frames, rate);
            pos += size;
          }
          pending = buf.slice(pos);
          return read();
        });
      }
      return read();
    }).then(retry, retry);
  };

  // One record: int16 interleaved → two Float32Arrays (a mono stream goes to both sides).
  MPCAudio.prototype.play = function (dv, nch, frames, rate) {
    var self = this;
    var l = new Float32Array(frames), r = new Float32Array(frames);
    for (var i = 0; i < frames; i++) {
      var a = dv.getInt16(16 + i * nch * 2, true) / 32768;
      l[i] = a;
      r[i] = nch > 1 ? dv.getInt16(16 + (i * nch + 1) * 2, true) / 32768 : a;
    }
    if (rate !== this.rate || !this.node) {
      this.setup(rate).then(function () { if (self.node) self.node.port.postMessage({ l: l, r: r }, [l.buffer, r.buffer]); });
      return;
    }
    this.node.port.postMessage({ l: l, r: r }, [l.buffer, r.buffer]);
  };

  window.MPCAudio = MPCAudio;
})();
