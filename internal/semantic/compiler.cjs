const fs = require("node:fs");
const path = require("node:path");
const { createRequire } = require("node:module");

const root = path.resolve(process.argv[1]);
const files = JSON.parse(fs.readFileSync(0, "utf8"));
const report = { basis: "typescript-compiler", status: "fallback", programs: 0, diagnostics: 0, calls: [], imports: [] };
const local = file => {
  const rel = path.relative(root, file);
  return rel && rel !== ".." && !rel.startsWith(".." + path.sep) && !path.isAbsolute(rel) ? rel.split(path.sep).join("/") : "";
};

function resolve() {
  const configs = new Set();
  const compilerRoots = new Set();
  for (const file of files) {
    for (let dir = path.dirname(path.join(root, file)); local(dir) || dir === root; dir = path.dirname(dir)) {
      if (fs.existsSync(path.join(dir, "package.json"))) compilerRoots.add(dir);
      const config = path.join(dir, "tsconfig.json");
      if (fs.existsSync(config)) configs.add(config);
      if (dir === root) break;
    }
  }
  let ts;
  for (const dir of [...compilerRoots, root]) {
    try {
      ts = createRequire(path.join(dir, "package.json"))("typescript");
      break;
    } catch (error) {
      if (error.code !== "MODULE_NOT_FOUND") throw error;
    }
  }
  if (!ts) {
    report.message = "TypeScript Compiler API is unavailable in the project";
    return;
  }
  if (!configs.size) {
    report.message = "no tsconfig.json found for checkout source";
    return;
  }

  function position(file, offset) {
    const name = local(file.fileName);
    if (!name) return null;
    const point = file.getLineAndCharacterOfPosition(offset);
    const start = file.getPositionOfLineAndCharacter(point.line, 0);
    return { file: name, line: point.line + 1, column: Buffer.byteLength(file.text.slice(start, offset)) + 1 };
  }
  const sourcePaths = new Set(files);
  const seen = new Set();
  function readConfig(config) {
    if (!local(config) || seen.has(config)) return;
    seen.add(config);
    const read = ts.readConfigFile(config, ts.sys.readFile);
    if (read.error) {
      report.diagnostics++;
      return;
    }
    const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, path.dirname(config), undefined, config);
    report.diagnostics += parsed.errors.length;
    for (const reference of parsed.projectReferences ?? []) {
      readConfig(ts.resolveProjectReferencePath(reference));
    }
    const rootNames = parsed.fileNames.filter(file => sourcePaths.has(local(file)));
    if (!rootNames.length) return;
    const options = { ...parsed.options, noEmit: true };
    const program = ts.createProgram({ rootNames, options, projectReferences: parsed.projectReferences });
    const checker = program.getTypeChecker();
    report.programs++;
    report.diagnostics += program.getSyntacticDiagnostics().length + program.getSemanticDiagnostics().length;
    const cache = ts.createModuleResolutionCache(path.dirname(config), file => file, options);
    for (const file of program.getSourceFiles()) {
      if (file.isDeclarationFile || !sourcePaths.has(local(file.fileName))) continue;
      function visit(node) {
        if ((ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) && node.moduleSpecifier && ts.isStringLiteralLike(node.moduleSpecifier)) {
          const target = ts.resolveModuleName(node.moduleSpecifier.text, file.fileName, options, ts.sys, cache).resolvedModule;
          const name = target && local(target.resolvedFileName);
          if (name && sourcePaths.has(name)) report.imports.push({ site: position(file, node.getStart(file)), target: { file: name, line: 1, column: 1 } });
        }
        if (ts.isCallExpression(node)) {
          const decl = checker.getResolvedSignature(node)?.declaration;
          // Require a concrete source body. Type-only contracts and overloads
          // without an implementation cannot prove a destination.
          if (decl?.body) {
            const target = position(decl.getSourceFile(), decl.getStart());
            if (target && sourcePaths.has(target.file)) report.calls.push({ site: position(file, node.getStart(file)), target });
          }
        }
        ts.forEachChild(node, visit);
      }
      visit(file);
    }
  }
  for (const config of configs) readConfig(config);
  report.status = report.programs ? (report.diagnostics ? "partial" : "resolved") : "fallback";
  if (!report.programs) report.message = "no configured TypeScript source files found";
  report.compilerVersion = ts.version;
}

try {
  resolve();
  process.stdout.write(JSON.stringify(report));
} catch (error) {
  process.stderr.write(String(error));
  process.exitCode = 1;
}
