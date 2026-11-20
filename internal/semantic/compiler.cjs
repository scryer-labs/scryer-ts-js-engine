const fs = require("node:fs");
const path = require("node:path");
const { createRequire } = require("node:module");
const os = require("node:os");

const root = path.resolve(process.argv[1]);
const files = JSON.parse(fs.readFileSync(0, "utf8"));
const report = { basis: "typescript-compiler", status: "fallback", programs: 0, diagnostics: 0, blockingDiagnostics: 0, usableForImpact: false, diagnosticMessages: [], calls: [], imports: [], endpoints: [] };
const local = file => {
  const rel = path.relative(root, file);
  return rel && rel !== ".." && !rel.startsWith(".." + path.sep) && !path.isAbsolute(rel) ? rel.split(path.sep).join("/") : "";
};

function resolve() {
  const configs = new Set();
  for (const file of files) {
    for (let dir = path.dirname(path.join(root, file)); local(dir) || dir === root; dir = path.dirname(dir)) {
      for (const name of ["tsconfig.json", "jsconfig.json"]) {
        const config = path.join(dir, name);
        if (fs.existsSync(config)) { configs.add(config); break; }
      }
      if (dir === root) break;
    }
  }
  const explicitTools = process.env.SCRYER_TS_TOOLCHAIN;
  const localTools = path.join(process.env.SCRYER_HOME || path.join(os.homedir(), ".config", "scryer"), "ts-toolchain");
  const loaders = [...new Set([explicitTools, localTools].filter(Boolean))]
    .map(dir => createRequire(path.resolve(dir, "package.json")));
  function dependency(name) {
    for (const load of loaders) {
      try { return load(name); }
      catch (error) { if (error.code !== "MODULE_NOT_FOUND") throw error; }
    }
  }
  const ts = dependency("typescript");
  if (!ts) {
    report.message = "TypeScript Compiler API is unavailable in the Scryer toolchain";
    return;
  }
  const inferJavaScript = !configs.size && files.length > 0 && files.every(file => /\.(?:[cm]?js|jsx)$/i.test(file));
  if (!configs.size && !inferJavaScript) {
    report.message = "no tsconfig.json or jsconfig.json found; inferred configuration is limited to JavaScript-only source";
    return;
  }
  report.configurationBasis = inferJavaScript ? "inferred-javascript" : "project-config";

  const svelteFiles = files.filter(file => file.endsWith(".svelte"));
  const virtual = new Map();
  let svelteMessage;
  if (svelteFiles.length) {
    const convert = dependency("svelte2tsx")?.svelte2tsx;
    const mapping = dependency("@jridgewell/trace-mapping");
    if (!convert || !mapping) svelteMessage = "Svelte compiler adapter or source-map reader is unavailable";
    else for (const file of svelteFiles) {
      const filename = path.join(root, file);
      try {
        const original = fs.readFileSync(filename, "utf8");
        const transformed = convert(original, { filename, isTsFile: /<script\b[^>]*\blang=["'](?:ts|typescript)["']/.test(original) });
        virtual.set(filename + ".__scryer.ts", { original, filename, transformed, map: new mapping.TraceMap(transformed.map),
          lines: original.split(/\r?\n/), mapPosition: mapping.originalPositionFor,
          segments: mapping.decodedMappings(new mapping.TraceMap(transformed.map)) });
      } catch (error) {
        svelteMessage = String(error);
      }
    }
  }
  report.svelteFiles = virtual.size;
  if (svelteMessage) report.svelteMessage = svelteMessage;

  function position(file, offset) {
    const generated = virtual.get(file.fileName);
    if (generated) {
      const at = file.getLineAndCharacterOfPosition(offset);
      const mapped = generated.mapPosition(generated.map, { line: at.line + 1, column: at.character });
      if (mapped.line == null || mapped.column == null) return null;
      const segment = generated.segments[at.line]?.find(segment => segment[0] === at.character && segment.length >= 4);
      if (!segment) return null;
      return {file: local(generated.filename), line: mapped.line,
        column: Buffer.byteLength(generated.lines[mapped.line - 1].slice(0, mapped.column)) + 1};
    }
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
      report.diagnostics++; report.blockingDiagnostics++;
      report.diagnosticMessages.push(ts.flattenDiagnosticMessageText(read.error.messageText,"\n"));
      return;
    }
    const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, path.dirname(config), undefined, config);
    report.diagnostics += parsed.errors.length;
    report.blockingDiagnostics += parsed.errors.length;
    for (const d of parsed.errors) report.diagnosticMessages.push(ts.flattenDiagnosticMessageText(d.messageText,"\n"));
    for (const reference of parsed.projectReferences ?? []) {
      readConfig(ts.resolveProjectReferencePath(reference));
    }
    analyzeProgram(parsed, path.dirname(config));
  }
  function analyzeProgram(parsed, directory) {
    const rootNames = parsed.fileNames.filter(file => sourcePaths.has(local(file)) && !file.endsWith(".svelte"));
    for (const [file, input] of virtual) {
      if (!path.relative(directory, input.filename).startsWith("..")) rootNames.push(file);
    }
    if (!rootNames.length) return;
    if (virtual.size) {
      for (const name of ['svelte2tsx/svelte-shims-v4.d.ts','svelte2tsx/svelte-jsx-v4.d.ts']) {
        for (const load of loaders) {
          try { const shim = load.resolve(name); if (!rootNames.includes(shim)) rootNames.push(shim); break; }
          catch (error) { if (error.code !== 'MODULE_NOT_FOUND' && error.code !== 'ERR_PACKAGE_PATH_NOT_EXPORTED') throw error; }
        }
      }
    }
    const options = { ...parsed.options, noEmit: true };
    if (virtual.size && !options.paths?.["$lib/*"] && fs.existsSync(path.join(directory, "src/lib")) &&
        fs.existsSync(path.join(directory, "svelte.config.js"))) {
      // Use the default layout only for a config with no explicit override in
      // its source or inherited TypeScript path aliases.
      const svelteConfig = fs.readFileSync(path.join(directory, "svelte.config.js"), "utf8");
      if (!/\b(?:alias|files)\s*:|\.\.\./.test(svelteConfig)) {
        options.paths = { ...options.paths, "$lib/*": [path.join(directory, "src/lib/*")] };
      }
    }
    const host = ts.createCompilerHost(options);
    const originalGet = host.getSourceFile.bind(host);
    host.getSourceFile = (name, version, ...args) => {
      const input = virtual.get(name);
      return input ? ts.createSourceFile(name, input.transformed.code, version, true, ts.ScriptKind.TS)
        : originalGet(name, version, ...args);
    };
    const program = ts.createProgram({ rootNames, options, projectReferences: parsed.projectReferences, host });
    const checker = program.getTypeChecker();
    report.programs++;
    const blocking = [...program.getSyntacticDiagnostics(), ...program.getOptionsDiagnostics(), ...program.getGlobalDiagnostics()];
    report.blockingDiagnostics += blocking.length;
    const semanticDiagnostics = program.getSemanticDiagnostics();
    const diagnostics = [...blocking, ...semanticDiagnostics];
    const affectedCall = node => diagnostics.some(d => d.file?.fileName === node.getSourceFile().fileName && d.start != null && d.start < node.end && d.start + Math.max(d.length || 0, 1) > node.getStart());
    report.diagnostics += diagnostics.length;
    for (const d of diagnostics.slice(0, 20)) {
      const at = d.file && d.start != null ? position(d.file,d.start) : null;
      report.diagnosticMessages.push(`${at ? at.file+':'+at.line+':'+at.column+': ' : ''}${ts.flattenDiagnosticMessageText(d.messageText,'\n')}`);
    }
    const cache = ts.createModuleResolutionCache(directory, file => file, options);
    for (const file of program.getSourceFiles()) {
      if (file.isDeclarationFile || (!sourcePaths.has(local(file.fileName)) && !virtual.has(file.fileName))) continue;
      function observeTauri(node) {
        const expression = node.expression;
        const name = ts.isPropertyAccessExpression(expression) ? expression.name : expression;
        const binding = checker.getSymbolAtLocation(name);
        for (const declaration of binding?.declarations ?? []) {
          if (!ts.isImportSpecifier(declaration) || declaration.isTypeOnly || declaration.parent.parent.isTypeOnly) continue;
          const module = declaration.parent.parent.parent.moduleSpecifier?.text;
          const exported = (declaration.propertyName || declaration.name).text;
          const kind = module === "@tauri-apps/api/core" && exported === "invoke" ? "command" :
            module === "@tauri-apps/api/event" && ["listen", "emit"].includes(exported) ? "event" : "";
          if (!kind) continue;
          let argument = node.arguments[0];
          if (argument && ts.isIdentifier(argument)) {
            const declarations = checker.getSymbolAtLocation(argument)?.declarations ?? [];
            if (declarations.length === 1 && ts.isVariableDeclaration(declarations[0]) &&
                (declarations[0].parent.flags & ts.NodeFlags.Const)) argument = declarations[0].initializer;
          }
          const site = position(file, node.getStart(file));
          if (site && argument && ts.isStringLiteralLike(argument) && argument.text) {
            report.endpoints.push({ ...site, kind, role: exported, name: argument.text });
          }
        }
      }
      function visit(node) {
        if ((ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) && node.moduleSpecifier && ts.isStringLiteralLike(node.moduleSpecifier)) {
          const target = ts.resolveModuleName(node.moduleSpecifier.text, file.fileName, options, ts.sys, cache).resolvedModule;
          const name = target && local(target.resolvedFileName);
          const site = position(file, node.getStart(file));
          if (site && name && sourcePaths.has(name)) report.imports.push({ site, target: { file: name, line: 1, column: 1 } });
        }
        if (ts.isCallExpression(node)) {
          if (virtual.has(file.fileName)) observeTauri(node);
          const decl = checker.getResolvedSignature(node)?.declaration;
          // Require a concrete source body. Type-only contracts and overloads
          // without an implementation cannot prove a destination.
          if (decl?.body && !affectedCall(node)) {
            const target = position(decl.getSourceFile(), decl.getStart());
            const site = position(file, node.getStart(file));
            if (site && target && sourcePaths.has(target.file)) report.calls.push({ site, target, targetName: decl.name ? position(decl.getSourceFile(), decl.name.getStart()) : null, end: position(file, node.expression.end - 1), callee: node.expression.getText(file) });
          }
        }
        ts.forEachChild(node, visit);
      }
      visit(file);
    }
  }
  if (inferJavaScript) {
    // Model source without creating or writing project configuration. Without a
    // declared environment, do not inject ambient @types or infer global scripts.
    const options = {allowJs: true, checkJs: false, noEmit: true, skipLibCheck: true,
      target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.NodeNext,
      moduleResolution: ts.ModuleResolutionKind.NodeNext, jsx: ts.JsxEmit.Preserve,
      moduleDetection: ts.ModuleDetectionKind.Force, types: []};
    report.message = "JavaScript-only source uses inferred NodeNext modules (ES2022, allowJs, checkJs=false, noEmit, no ambient types). Unbound or external calls remain boundaries.";
    analyzeProgram({fileNames: files.map(file => path.join(root, file)), options}, root);
  } else {
    for (const config of configs) readConfig(config);
  }
  report.usableForImpact = report.programs > 0 && !report.blockingDiagnostics && !svelteMessage;
  report.status = report.programs ? ((report.diagnostics || svelteMessage) ? "partial" : "resolved") : "fallback";
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
