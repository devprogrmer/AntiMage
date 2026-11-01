import {
	Badge,
	Box,
	Button,
	Code,
	Container,
	Flex,
	Grid,
	Heading,
	HStack,
	Input,
	InputGroup,
	InputLeftElement,
	Link,
	SimpleGrid,
	Spinner,
	Stack,
	Text,
	VStack,
} from "@chakra-ui/react";
import {
	ArrowTopRightOnSquareIcon,
	BookOpenIcon,
	ClipboardDocumentIcon,
	MagnifyingGlassIcon,
} from "@heroicons/react/24/outline";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useHref } from "react-router-dom";

type Parameter = {
	name: string;
	in: string;
	required?: boolean;
	description?: string;
	schema?: { type?: string };
};
type Operation = {
	tags?: string[];
	summary?: string;
	description?: string;
	operationId?: string;
	security?: unknown[];
	parameters?: Parameter[];
	requestBody?: { description?: string; required?: boolean };
	responses?: Record<string, { description?: string }>;
};
type OpenAPIDocument = {
	info?: { title?: string; version?: string; description?: string };
	paths?: Record<string, Record<string, Operation>>;
	tags?: { name: string; description?: string }[];
};
type Endpoint = {
	method: string;
	path: string;
	operation: Operation;
	tag: string;
};
type Language = "fa" | "en" | "ru";

const copyByLanguage = {
	fa: {
		title: "مرجع API AntiMage",
		intro:
			"عملیات پنل، توضیح دسترسی و ورودی‌ها را جست‌وجو کنید. این راهنما برای توسعه‌ی اتصال‌های اختصاصی AntiMage است.",
		search: "جست‌وجوی مسیر، عملیات یا توضیحات…",
		all: "همه بخش‌ها",
		loading: "در حال دریافت مرجع API…",
		unavailable: "مرجع API در این نصب فعال نیست.",
		enable: "برای نمایش مرجع، API docs را در تنظیمات پنل فعال کنید.",
		endpoints: "عملیات",
		operation: "عملیات انتخاب‌شده",
		auth: "دسترسی",
		params: "پارامترها",
		responses: "پاسخ‌ها",
		body: "درخواست",
		noParams: "این عملیات پارامتر مسیر یا query مستندی ندارد.",
		noBody: "برای این عملیات بدنه درخواست تعریف نشده است.",
		public: "عمومی",
		required: "الزامی",
		optional: "اختیاری",
		copy: "کپی مسیر",
		copied: "کپی شد",
		schema: "دریافت OpenAPI JSON",
		guide: "راهنمای پنل",
		back: "ورود به پنل",
		empty: "عملیاتی مطابق جست‌وجو پیدا نشد.",
		sample: "مسیر API",
		securityNote:
			"درخواست‌های تغییر‌دهنده را فقط پس از بررسی مجوز، داده و اثر جانبی اجرا کنید.",
	},
	en: {
		title: "AntiMage API reference",
		intro:
			"Search panel operations, access requirements, and inputs. This reference is for integrations built for AntiMage.",
		search: "Search paths, operations, or descriptions…",
		all: "All sections",
		loading: "Loading the API reference…",
		unavailable: "The API reference is disabled on this installation.",
		enable: "Enable API docs in panel settings to display the reference.",
		endpoints: "operations",
		operation: "Selected operation",
		auth: "Access",
		params: "Parameters",
		responses: "Responses",
		body: "Request",
		noParams: "This operation has no documented path or query parameters.",
		noBody: "No request body is defined for this operation.",
		public: "Public",
		required: "Required",
		optional: "Optional",
		copy: "Copy path",
		copied: "Copied",
		schema: "Download OpenAPI JSON",
		guide: "Panel guide",
		back: "Open panel",
		empty: "No operations match this search.",
		sample: "API route",
		securityNote:
			"Review permissions, data, and side effects before sending a request that changes state.",
	},
	ru: {
		title: "Справочник API AntiMage",
		intro:
			"Ищите операции панели, требования доступа и входные параметры. Справочник предназначен для интеграций с AntiMage.",
		search: "Поиск по маршрутам и операциям…",
		all: "Все разделы",
		loading: "Загрузка справочника API…",
		unavailable: "Справочник API отключён в этой установке.",
		enable: "Включите документацию API в настройках панели.",
		endpoints: "операций",
		operation: "Выбранная операция",
		auth: "Доступ",
		params: "Параметры",
		responses: "Ответы",
		body: "Запрос",
		noParams: "Для операции не описаны параметры пути или запроса.",
		noBody: "Тело запроса не определено.",
		public: "Публичный",
		required: "Обязательно",
		optional: "Необязательно",
		copy: "Копировать путь",
		copied: "Скопировано",
		schema: "Скачать OpenAPI JSON",
		guide: "Руководство панели",
		back: "Открыть панель",
		empty: "Операции не найдены.",
		sample: "Маршрут API",
		securityNote:
			"Перед запросом с изменением данных проверьте права и побочные эффекты.",
	},
};

const methodColors: Record<string, string> = {
	get: "blue",
	post: "green",
	put: "orange",
	patch: "purple",
	delete: "red",
};
const getLanguage = (value: string): Language =>
	value.startsWith("fa") ? "fa" : value.startsWith("ru") ? "ru" : "en";
const readOperations = (document: OpenAPIDocument): Endpoint[] =>
	Object.entries(document.paths || {}).flatMap(([path, methods]) =>
		Object.entries(methods).flatMap(([method, operation]) => {
			if (
				!["get", "post", "put", "patch", "delete"].includes(
					method.toLowerCase(),
				) ||
				!operation ||
				typeof operation !== "object"
			)
				return [];
			return [
				{
					method: method.toUpperCase(),
					path,
					operation,
					tag: operation.tags?.[0] || "General",
				},
			];
		}),
	);

export const ApiDocsPage = () => {
	const { i18n } = useTranslation();
	const language = getLanguage(i18n.resolvedLanguage || i18n.language || "en");
	const copy = copyByLanguage[language];
	const schemaUrl = "/openapi.json";
	const guideUrl = useHref("/guide");
	const panelUrl = useHref("/");
	const [document, setDocument] = useState<OpenAPIDocument | null>(null);
	const [failed, setFailed] = useState(false);
	const [query, setQuery] = useState("");
	const [activeTag, setActiveTag] = useState("");
	const [selectedKey, setSelectedKey] = useState("");
	const [copied, setCopied] = useState(false);
	useEffect(() => {
		let cancelled = false;
		void fetch(schemaUrl, {
			headers: { Accept: "application/json" },
			cache: "no-store",
		})
			.then(async (response) => {
				if (!response.ok) throw new Error("API schema is unavailable");
				return response.json() as Promise<OpenAPIDocument>;
			})
			.then((value) => {
				if (!cancelled) setDocument(value);
			})
			.catch(() => {
				if (!cancelled) setFailed(true);
			});
		return () => {
			cancelled = true;
		};
	}, []);
	const operations = useMemo(
		() => (document ? readOperations(document) : []),
		[document],
	);
	const tags = useMemo(
		() =>
			(document?.tags || [])
				.map((tag) => tag.name)
				.filter((tag) => operations.some((item) => item.tag === tag)),
		[document, operations],
	);
	const filtered = useMemo(
		() =>
			operations.filter(
				(item) =>
					(!activeTag || item.tag === activeTag) &&
					`${item.path} ${item.method} ${item.tag} ${item.operation.summary || ""} ${item.operation.description || ""}`
						.toLowerCase()
						.includes(query.toLowerCase()),
			),
		[operations, activeTag, query],
	);
	const selected =
		filtered.find((item) => `${item.method} ${item.path}` === selectedKey) ||
		filtered[0];
	const direction = language === "fa" ? "rtl" : "ltr";
	const copyPath = async () => {
		if (!selected) return;
		await navigator.clipboard.writeText(selected.path);
		setCopied(true);
		window.setTimeout(() => setCopied(false), 1500);
	};
	if (!document && !failed)
		return (
			<Box
				minH="100vh"
				bg="#080b12"
				color="white"
				display="grid"
				placeItems="center"
			>
				<VStack>
					<Spinner color="pink.300" />
					<Text>{copy.loading}</Text>
				</VStack>
			</Box>
		);
	if (failed || !document)
		return (
			<Box minH="100vh" bg="#080b12" color="white" dir={direction} p={8}>
				<Container maxW="760px">
					<VStack
						align="start"
						p={8}
						bg="#111a27"
						border="1px solid"
						borderColor="whiteAlpha.200"
						borderRadius="2xl"
					>
						<Heading>{copy.unavailable}</Heading>
						<Text color="whiteAlpha.700">{copy.enable}</Text>
						<Button as="a" href={panelUrl} colorScheme="pink">
							{copy.back}
						</Button>
					</VStack>
				</Container>
			</Box>
		);
	return (
		<Box minH="100vh" bg="#080b12" color="#e8edf7" dir={direction}>
			<Container maxW="1600px" py={{ base: 4, md: 8 }} px={{ base: 4, md: 8 }}>
				<Flex
					justify="space-between"
					align="center"
					gap={4}
					borderBottom="1px solid"
					borderColor="whiteAlpha.200"
					pb={5}
					mb={7}
					wrap="wrap"
				>
					<HStack spacing={3}>
						<Box p={2} bg="#28162a" borderRadius="lg">
							<ShieldAccessIcon />
						</Box>
						<Box>
							<Text color="#f05aa3" fontSize="xs" letterSpacing=".12em">
								ANTIMAGE DEVELOPER CENTER
							</Text>
							<Heading size="md">{copy.title}</Heading>
						</Box>
					</HStack>
					<HStack>
						<Button
							as="a"
							href={guideUrl}
							size="sm"
							variant="outline"
							leftIcon={<BookOpenIcon width={16} />}
						>
							{copy.guide}
						</Button>
						<Button
							as="a"
							href={schemaUrl}
							target="_blank"
							rel="noreferrer"
							size="sm"
							colorScheme="pink"
							rightIcon={<ArrowTopRightOnSquareIcon width={15} />}
						>
							{copy.schema}
						</Button>
					</HStack>
				</Flex>
				<Grid
					templateColumns={{
						base: "1fr",
						xl: "minmax(0, .95fr) minmax(0, 1.25fr)",
					}}
					gap={7}
				>
					<Box>
						<Text
							color="whiteAlpha.700"
							fontSize={{ base: "md", md: "lg" }}
							lineHeight="1.8"
							mb={5}
						>
							{copy.intro}
						</Text>
						<SimpleGrid columns={3} spacing={3} mb={5}>
							<Metric title={copy.endpoints} value={operations.length} />
							<Metric
								title={
									language === "fa"
										? "بخش"
										: language === "ru"
											? "разделов"
											: "sections"
								}
								value={tags.length}
							/>
							<Metric title="OpenAPI" value={document.info?.version || "3.0"} />
						</SimpleGrid>
						<InputGroup mb={3}>
							<InputLeftElement pointerEvents="none">
								<MagnifyingGlassIcon width={17} />
							</InputLeftElement>
							<Input
								value={query}
								onChange={(event) => setQuery(event.target.value)}
								placeholder={copy.search}
								bg="#101927"
								borderColor="whiteAlpha.300"
							/>
						</InputGroup>
						<Flex gap={2} wrap="wrap" mb={3}>
							<Button
								size="xs"
								onClick={() => setActiveTag("")}
								variant={!activeTag ? "solid" : "outline"}
								colorScheme={!activeTag ? "pink" : "gray"}
							>
								{copy.all}
							</Button>
							{tags.map((tag) => (
								<Button
									key={tag}
									size="xs"
									onClick={() => setActiveTag(activeTag === tag ? "" : tag)}
									variant={activeTag === tag ? "solid" : "outline"}
									colorScheme={activeTag === tag ? "pink" : "gray"}
								>
									{tag}
								</Button>
							))}
						</Flex>
						<VStack
							align="stretch"
							spacing={2}
							maxH={{ base: "340px", xl: "calc(100vh - 370px)" }}
							overflowY="auto"
							pe={1}
						>
							{filtered.map((item) => (
								<Button
									key={`${item.method}-${item.path}`}
									onClick={() => setSelectedKey(`${item.method} ${item.path}`)}
									justifyContent="start"
									textAlign="start"
									h="auto"
									minH="58px"
									py={2}
									px={3}
									whiteSpace="normal"
									bg={
										selected?.path === item.path &&
										selected.method === item.method
											? "#261b30"
											: "#101927"
									}
									border="1px solid"
									borderColor={
										selected?.path === item.path &&
										selected.method === item.method
											? "#b83b79"
											: "whiteAlpha.100"
									}
									_hover={{ borderColor: "pink.300" }}
								>
									<HStack align="start" w="full">
										<Badge
											minW="62px"
											textAlign="center"
											colorScheme={
												methodColors[item.method.toLowerCase()] || "gray"
											}
										>
											{item.method}
										</Badge>
										<Box minW={0}>
											<Text fontFamily="mono" fontSize="xs" noOfLines={1}>
												{item.path}
											</Text>
											<Text fontSize="xs" color="whiteAlpha.600" noOfLines={1}>
												{item.operation.summary || item.tag}
											</Text>
										</Box>
									</HStack>
								</Button>
							))}
							{filtered.length === 0 && (
								<Text p={4} color="whiteAlpha.600">
									{copy.empty}
								</Text>
							)}
						</VStack>
					</Box>
					{selected ? (
						<Box
							border="1px solid"
							borderColor="whiteAlpha.200"
							bg="#101722"
							borderRadius="2xl"
							overflow="hidden"
							alignSelf="start"
						>
							<Flex
								p={{ base: 4, md: 6 }}
								justify="space-between"
								align="start"
								gap={4}
								borderBottom="1px solid"
								borderColor="whiteAlpha.200"
							>
								<Box>
									<Badge
										colorScheme={
											methodColors[selected.method.toLowerCase()] || "gray"
										}
										mb={3}
									>
										{selected.method}
									</Badge>
									<Heading size={{ base: "sm", md: "md" }}>
										{selected.operation.summary ||
											selected.operation.operationId ||
											selected.path}
									</Heading>
									<Code
										display="block"
										bg="#080e18"
										color="#ffa1ce"
										p={3}
										mt={3}
										borderRadius="md"
										whiteSpace="normal"
										wordBreak="break-all"
									>
										{selected.method} {selected.path}
									</Code>
								</Box>
								<Button
									size="sm"
									variant="outline"
									onClick={() => void copyPath()}
									leftIcon={<ClipboardDocumentIcon width={16} />}
								>
									{copied ? copy.copied : copy.copy}
								</Button>
							</Flex>
							<Stack p={{ base: 4, md: 6 }} spacing={6}>
								<Box>
									<Text color="#ff87be" fontWeight="bold" mb={2}>
										{copy.operation}
									</Text>
									<Text
										whiteSpace="pre-line"
										color="whiteAlpha.800"
										lineHeight="1.8"
									>
										{selected.operation.description ||
											selected.operation.summary ||
											selected.operation.operationId}
									</Text>
								</Box>
								<Box>
									<Text color="#ff87be" fontWeight="bold" mb={2}>
										{copy.auth}
									</Text>
									<Text fontSize="sm" color="whiteAlpha.800">
										{selected.operation.security === undefined ? "" : ""}
										{selected.operation.description?.match(
											/Authentication:[^\n]*/i,
										)?.[0] ||
											document.info?.description?.match(
												/Authentication:[^\n]*/i,
											)?.[0] ||
											(selected.operation.security?.length === 0
												? copy.public
												: "Authenticated administrator; access is limited by the active admin permissions.")}
									</Text>
								</Box>
								<Box>
									<Text color="#ff87be" fontWeight="bold" mb={3}>
										{copy.params}
									</Text>
									{selected.operation.parameters?.length ? (
										<VStack align="stretch" spacing={2}>
											{selected.operation.parameters.map((parameter) => (
												<Flex
													key={`${parameter.in}-${parameter.name}`}
													justify="space-between"
													gap={3}
													p={3}
													bg="#080e18"
													borderRadius="md"
													wrap="wrap"
												>
													<HStack>
														<Code colorScheme="pink">{parameter.name}</Code>
														<Badge>{parameter.in}</Badge>
													</HStack>
													<Text fontSize="sm" color="whiteAlpha.700" flex="1">
														{parameter.description ||
															parameter.schema?.type ||
															"string"}
													</Text>
													<Badge
														colorScheme={parameter.required ? "orange" : "gray"}
													>
														{parameter.required ? copy.required : copy.optional}
													</Badge>
												</Flex>
											))}
										</VStack>
									) : (
										<Text fontSize="sm" color="whiteAlpha.600">
											{copy.noParams}
										</Text>
									)}
								</Box>
								<Box>
									<Text color="#ff87be" fontWeight="bold" mb={3}>
										{copy.body}
									</Text>
									{selected.operation.requestBody ? (
										<Text fontSize="sm" color="whiteAlpha.800">
											{selected.operation.requestBody.description ||
												(selected.operation.requestBody.required
													? copy.required
													: copy.optional)}
										</Text>
									) : (
										<Text fontSize="sm" color="whiteAlpha.600">
											{copy.noBody}
										</Text>
									)}
								</Box>
								<Box>
									<Text color="#ff87be" fontWeight="bold" mb={3}>
										{copy.responses}
									</Text>
									<SimpleGrid columns={{ base: 1, md: 2 }} spacing={2}>
										{Object.entries(selected.operation.responses || {}).map(
											([status, response]) => (
												<Flex
													key={status}
													gap={3}
													p={3}
													bg="#080e18"
													borderRadius="md"
													align="center"
												>
													<Badge
														colorScheme={
															status.startsWith("2")
																? "green"
																: status.startsWith("4")
																	? "orange"
																	: "red"
														}
													>
														{status}
													</Badge>
													<Text fontSize="sm" color="whiteAlpha.700">
														{response.description || ""}
													</Text>
												</Flex>
											),
										)}
									</SimpleGrid>
								</Box>
								<Text
									color="whiteAlpha.500"
									fontSize="xs"
									borderTop="1px solid"
									borderColor="whiteAlpha.200"
									pt={4}
								>
									{copy.securityNote}
								</Text>
							</Stack>
						</Box>
					) : null}
				</Grid>
				<Flex
					mt={8}
					justify="space-between"
					color="whiteAlpha.500"
					fontSize="xs"
				>
					<Text>
						{document.info?.title} · {document.info?.version}
					</Text>
					<Link href={guideUrl} color="pink.200">
						{copy.guide}
					</Link>
				</Flex>
			</Container>
		</Box>
	);
};

const Metric = ({
	title,
	value,
}: {
	title: string;
	value: string | number;
}) => (
	<Box
		bg="#101927"
		border="1px solid"
		borderColor="whiteAlpha.100"
		p={3}
		borderRadius="lg"
	>
		<Text color="whiteAlpha.600" fontSize="xs">
			{title}
		</Text>
		<Text fontWeight="bold" mt={1}>
			{value}
		</Text>
	</Box>
);
const ShieldAccessIcon = () => <BookOpenIcon width={23} color="#f05aa3" />;
