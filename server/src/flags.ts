// Go 의 flag 패키지와 같은 규칙으로 인자를 읽는다.
//
// node:util 의 parseArgs 를 쓰지 않는 이유는 하나다. Go 의 bool 플래그는
// `--open=false` 만 받고 `--open false` 는 받지 않는데, parseArgs 의 boolean
// 은 정반대로 `--open=false` 를 거부한다. README 와 dev.mjs 가 이미 `=` 형태를
// 쓰고 있으므로 여기서 규칙을 바꾸면 두 트랙의 명령이 갈라진다.

export interface Flags {
	addr: string;
	port: number;
	env: string;
	open: boolean;
	verbose: boolean;
	/** --port 를 직접 준 경우. 주지 않았을 때만 다음 포트로 넘어간다. */
	portWasChosen: boolean;
}

const usage = `사용법: skills-dashboard [옵션]

  --addr string    바인드 주소 (기본 127.0.0.1)
  --port int       포트 (기본 8080, 명시하지 않으면 8085 까지 시도)
  --env string     AWS 자격증명이 든 .env 경로
  --open           시작할 때 브라우저를 연다 (기본 true, 끄려면 --open=false)
  --verbose        debug 로그
`;

const booleans = new Set(['open', 'verbose']);

function fail(message: string): never {
	process.stderr.write(`${message}\n\n${usage}`);
	process.exit(2);
}

function parseBool(name: string, raw: string): boolean {
	switch (raw) {
		case '1':
		case 't':
		case 'T':
		case 'true':
		case 'TRUE':
		case 'True':
			return true;
		case '0':
		case 'f':
		case 'F':
		case 'false':
		case 'FALSE':
		case 'False':
			return false;
		default:
			return fail(`invalid boolean value ${JSON.stringify(raw)} for -${name}`);
	}
}

export function parseFlags(argv: readonly string[]): Flags {
	const flags: Flags = {
		addr: '127.0.0.1',
		port: 8080,
		env: '',
		open: true,
		verbose: false,
		portWasChosen: false
	};

	for (let i = 0; i < argv.length; i++) {
		const arg = argv[i] as string;

		// Go 도 첫 비플래그 인자에서 파싱을 멈춘다.
		if (!arg.startsWith('-') || arg === '-' || arg === '--') break;

		const body = arg.replace(/^--?/, '');
		const eq = body.indexOf('=');
		const name = eq >= 0 ? body.slice(0, eq) : body;
		let value = eq >= 0 ? body.slice(eq + 1) : undefined;

		if (value === undefined && !booleans.has(name)) {
			value = argv[i + 1];
			if (value === undefined) fail(`flag needs an argument: -${name}`);
			i++;
		}

		switch (name) {
			case 'addr':
				flags.addr = value as string;
				break;
			case 'port': {
				const port = Number(value);
				if (!Number.isInteger(port) || port < 1 || port > 65535) {
					fail(`invalid value ${JSON.stringify(value)} for flag -port`);
				}
				flags.port = port;
				flags.portWasChosen = true;
				break;
			}
			case 'env':
				flags.env = value as string;
				break;
			case 'open':
				flags.open = value === undefined ? true : parseBool(name, value);
				break;
			case 'verbose':
				flags.verbose = value === undefined ? true : parseBool(name, value);
				break;
			case 'help':
			case 'h':
				process.stderr.write(usage);
				process.exit(0);
				break;
			default:
				fail(`flag provided but not defined: -${name}`);
		}
	}

	return flags;
}
