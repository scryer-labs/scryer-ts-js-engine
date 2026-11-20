"""Core/engine integration for JavaScript-only projects without tsconfig."""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

cli, output = Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve()
output.mkdir(parents=True, exist_ok=True)


def run(*arguments, success=True):
    process = subprocess.run([str(a) for a in arguments], capture_output=True, text=True)
    assert (process.returncode == 0) == success, (arguments, process.stdout[-1500:], process.stderr[-5000:])
    return process


for project in map(Path, sys.argv[3:]):
    process = run(cli, 'project', 'scan', project, '--facts', 'files,call-graph', '--resolve', '--json', '--compact')
    report = json.loads(process.stdout)
    facts = [item for item in report['engineFacts'] if item.get('engine') == 'ts-js']
    assert facts and all(item['status'] == 'collected' for item in facts), facts
    for fact in facts:
        semantic = fact['data']['semantic']
        assert semantic['status'] == 'resolved' and semantic['configurationBasis'] == 'inferred-javascript', semantic
    (output / (project.name + '-scan.json')).write_text(process.stdout)

with tempfile.TemporaryDirectory(prefix='scryer-inferred-js-') as folder:
    root = Path(folder).resolve()
    def git(*args):
        return run('git', '-C', root, *args).stdout.strip()
    (root / 'package.json').write_text(json.dumps({'name': 'inferred-fixture', 'type': 'module', 'scripts': {'test': 'node --test value.test.js'}}))
    (root / 'value.js').write_text('export function value(){return 1;}\nexport function caller(){return value();}\nexport function unused(){return 1;}\n')
    (root / 'value.test.js').write_text('import {test} from "node:test";\nimport assert from "node:assert/strict";\nimport {caller} from "./value.js";\ntest("value",()=>assert.ok(caller()>0));\n')
    git('init', '-q'); git('config', 'user.name', 'Inference fixture'); git('config', 'user.email', 'fixture@example.invalid')
    git('add', '.'); git('commit', '-qm', 'before')
    source = root / 'value.js'; source.write_text(source.read_text().replace('return 1', 'return 2'))
    git('add', '.'); git('commit', '-qm', 'after')
    process = run(cli, 'project', 'analyze', root, '--resolve', '--operations', 'impact,coverage', '--json', '--compact', '-o', output / 'inferred.html')
    report = json.loads(process.stdout)
    assert report['execution']['status'] == 'SUCCEEDED' and report['coverageReport']['status'] == 'collected', report
    for key in ['beforeResolution', 'afterResolution']:
        assert report['extensions'][key]['configurationBasis'] == 'inferred-javascript'
    assert any(edge['kind'] == 'SCAN_RESOLVED' for edge in report['after']['edges']), report['after']
    assert any(method['status'] == 'NOT_EXECUTED' for dataset in report['matching']['datasets'] for method in dataset['methods'])
    assert git('status', '--porcelain') == ''
    assert not (root / 'tsconfig.json').exists() and not (root / 'jsconfig.json').exists()
    (output / 'inferred-impact.json').write_text(process.stdout)
    # Resolving syntax is not evidence of an executable test lifecycle.
    (root / 'package.json').unlink()
    source.write_text(source.read_text().replace('return 2', 'return 3'))
    git('add', '-u'); git('commit', '-qm', 'no test configuration')
    failed = run(cli, 'project', 'analyze', root, '--resolve', '--json', '-o', output / 'must-not-exist.html', success=False)
    partial = json.loads(failed.stdout)
    assert partial['status']=='partial' and partial['after']['nodes'] and (output / 'must-not-exist.html').exists()
    assert any('No original-source coverage mapped' in stage.get('message','') for outcome in partial['engineOutcomes'] for stage in outcome['stages']), partial['engineOutcomes']
    (output / 'missing-tests.txt').write_text(json.dumps(partial['engineOutcomes'],indent=2)+'\n'+failed.stderr)
print('Real JavaScript sources, inferred impact/coverage and missing-test rejection passed.')
