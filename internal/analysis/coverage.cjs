const fs = require('node:fs');
const path = require('node:path');
const {fileURLToPath} = require('node:url');
const crypto = require('node:crypto');
const input = JSON.parse(fs.readFileSync(process.argv[1], 'utf8'));
const root = fs.realpathSync(input.root);
const files = new Map();
const notes = [];
const hash = crypto.createHash('sha256');
let omitted = 0;
function local(filename) {
  if (filename.startsWith('file:')) { try { filename = fileURLToPath(filename); } catch { return ''; } }
  if (!path.isAbsolute(filename)) filename = path.resolve(root, filename);
  try { filename = fs.realpathSync(filename); } catch { return ''; }
  const relative = path.relative(root, filename);
  if (!relative || relative === '..' || relative.startsWith('..'+path.sep) || path.isAbsolute(relative)) return '';
  if (relative.split(path.sep).some(p => ['node_modules','.svelte-kit','dist','build','coverage'].includes(p))) return '';
  return relative.split(path.sep).join('/');
}
function fileFor(relative) {
  if (!files.has(relative)) files.set(relative, {path:relative, regions:[], summary:{functions:null}});
  return files.get(relative);
}
function position(source, offset) {
  if (!Number.isSafeInteger(offset) || offset < 0 || offset > source.length) throw Error('Invalid V8 source offset');
  const prefix = source.slice(0, offset), lineStart = prefix.lastIndexOf('\n') + 1;
  return {line: prefix.split('\n').length, column: Buffer.byteLength(prefix.slice(lineStart), 'utf8') + 1};
}
function mappedPosition(source, point) {
  const lines = source.split('\n');
  if (!point || !Number.isSafeInteger(point.line) || point.line < 1 || point.line > lines.length || !Number.isSafeInteger(point.column) || point.column < 0 || point.column > lines[point.line-1].length) throw Error('Invalid Istanbul source position');
  return {line:point.line,column:Buffer.byteLength(lines[point.line-1].slice(0,point.column),'utf8')+1};
}
function count(value) { if (!Number.isSafeInteger(value) || value < 0) throw Error('Invalid coverage counter'); return value; }
function merge(file, region) {
  const key = `${region.start.line}:${region.start.column}-${region.end.line}:${region.end.column}`;
  if (!file.index) file.index = new Map();
  if (file.index.has(key)) file.regions[file.index.get(key)].count += region.count;
  else { file.index.set(key,file.regions.length); file.regions.push(region); }
}
for (const profile of input.profiles) {
  const raw = fs.readFileSync(profile); hash.update(raw);
  const document = JSON.parse(raw);
  if (input.kind === 'istanbul') {
    if (!document || Array.isArray(document) || typeof document !== 'object') throw Error('Expected Istanbul file coverage map');
    for (const [filename, record] of Object.entries(document)) {
      if (!record.statementMap || !record.s) throw Error('Invalid Istanbul file coverage');
      const relative = local(record.path || filename); if (!relative) continue;
      const source = fs.readFileSync(path.join(root,relative),'utf8'); const file = fileFor(relative);
      for (const [id, location] of Object.entries(record.statementMap)) {
        merge(file,{start:mappedPosition(source,location.start),end:mappedPosition(source,location.end),count:count(record.s[id])});
      }
      if (record.f) {
        const hits=Object.values(record.f).map(count); file.summary.functions={count:hits.length,covered:hits.filter(n=>n>0).length};
      }
    }
  } else {
    if (!Array.isArray(document.result)) throw Error('Expected NODE_V8_COVERAGE JSON');
    for (const script of document.result) {
      const relative=local(script.url || ''); if (!relative) continue;
      // Raw native JavaScript is supported. Transformed sources require the
      // runner's remapped Istanbul output; never apply generated offsets to TS.
      if (!/\.(?:mjs|cjs|js)$/.test(relative)) { omitted++; continue; }
      const source=fs.readFileSync(path.join(root,relative),'utf8');
      if (document['source-map-cache']?.[script.url]) { omitted++; continue; }
      const file=fileFor(relative); const ranges=[];
      for (const fn of script.functions || []) for (const range of fn.ranges || []) {
        if (range.endOffset > source.length) throw Error('V8 offsets do not match the original source');
        ranges.push({...range,count:count(range.count)});
      }
      const points=[...new Set(ranges.flatMap(r=>[r.startOffset,r.endOffset]))].sort((a,b)=>a-b);
      for (let i=0;i+1<points.length;i++) {
        const start=points[i],end=points[i+1];if (start===end)continue;
        // Specific function/block ranges override the enclosing script range.
        const candidates=ranges.filter(r=>r.startOffset<=start&&r.endOffset>=end).sort((a,b)=>(a.endOffset-a.startOffset)-(b.endOffset-b.startOffset));
        if (candidates.length) merge(file,{start:position(source,start),end:position(source,end),count:candidates[0].count});
      }
    }
  }
}
if (omitted) notes.push(`${omitted} transformed/non-JavaScript V8 source records require a remapped Istanbul report and were not attributed.`);
for (const file of files.values()) delete file.index;
process.stdout.write(JSON.stringify({files:[...files.values()],notes,omitted,sha256:hash.digest('hex')}));
