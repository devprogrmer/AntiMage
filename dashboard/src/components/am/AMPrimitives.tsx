import {
	Alert,
	AlertDescription,
	AlertIcon,
	AlertTitle,
	Badge,
	Box,
	Button,
	Drawer as ChakraDrawer,
	DrawerBody,
	DrawerCloseButton,
	DrawerContent,
	DrawerHeader,
	DrawerOverlay,
	Flex,
	Heading,
	HStack,
	Input,
	Progress,
	Table,
	Tbody,
	Td,
	Th,
	Thead,
	Tr,
	Menu,
	MenuButton,
	MenuItem,
	MenuList,
	SimpleGrid,
	Spinner,
	Text,
	VStack,
	type DrawerProps as ChakraDrawerProps,
	type BoxProps,
} from "@chakra-ui/react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { EllipsisVerticalIcon } from "@heroicons/react/24/outline";

const panel = {
	bg: "var(--am-native-surface, var(--am-panel-surface, white))",
	borderColor: "var(--am-native-border, var(--am-panel-border, #dbe3e8))",
	color: "var(--am-native-text, var(--am-panel-text, #14212b))",
};

export function AMPage({ children, ...props }: BoxProps) {
	return <VStack align="stretch" spacing={{ base: 5, xl: 7 }} {...props}>{children}</VStack>;
}

export function AMPageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
	return <Flex align={{ base: "start", md: "center" }} justify="space-between" gap={4} wrap="wrap"><HStack align="stretch" spacing={4}><Box w="3px" borderRadius="full" bg="var(--am-native-accent)" aria-hidden="true" /><Box><Heading size="lg" letterSpacing="-.035em">{title}</Heading>{description && <Text mt={2} color="var(--am-native-muted, var(--am-panel-text-muted))">{description}</Text>}</Box></HStack>{actions}</Flex>;
}

export function AMSection({ title, description, children, ...props }: { title: string; description?: string; children: ReactNode } & BoxProps) {
	return <Box as="section" border="1px solid" borderRadius="xl" p={{ base: 4, md: 5 }} {...panel} {...props}><Box mb={4}><Heading size="sm">{title}</Heading>{description && <Text mt={1} fontSize="sm" color="var(--am-native-muted, var(--am-panel-text-muted))">{description}</Text>}</Box>{children}</Box>;
}

export function AMMetric({ label, value, hint, tone = "neutral" }: { label: string; value: ReactNode; hint?: string; tone?: "neutral" | "good" | "warning" | "critical" }) {
	const colors = { neutral: "gray", good: "green", warning: "orange", critical: "red" } as const;
	return <Box border="1px solid" borderInlineStart="3px solid" borderInlineStartColor={tone==="critical"?"red.400":tone==="warning"?"orange.300":"var(--am-native-accent)"} borderRadius="xl" p={4} {...panel}><HStack justify="space-between"><Text fontSize="sm" color="var(--am-native-muted, var(--am-panel-text-muted))">{label}</Text><Badge colorScheme={colors[tone]}>{tone}</Badge></HStack><Text dir="ltr" mt={3} fontSize="2xl" fontWeight="bold" sx={{ fontVariantNumeric:"tabular-nums", unicodeBidi:"isolate" }}>{value}</Text>{hint && <Text mt={1} fontSize="xs" color="var(--am-native-muted, var(--am-panel-text-muted))">{hint}</Text>}</Box>;
}

export function AMStatusBadge({ state }: { state: string }) {
	const scheme = state === "healthy" || state === "completed" ? "green" : state === "critical" || state === "failed" ? "red" : state === "warning" || state === "running" ? "orange" : "gray";
	return <Badge colorScheme={scheme}>{state}</Badge>;
}

export function AMAlert({ title, children, status = "info" }: { title: string; children: ReactNode; status?: "info" | "warning" | "error" | "success" }) {
	return <Alert status={status} borderRadius="lg"><AlertIcon /><Box><AlertTitle>{title}</AlertTitle><AlertDescription>{children}</AlertDescription></Box></Alert>;
}

export function AMEmptyState({ title, detail, action }: { title: string; detail: string; action?: ReactNode }) {
	return <VStack py={10} px={5} border="1px dashed" borderRadius="xl" borderColor={panel.borderColor} spacing={3}><Heading size="sm">{title}</Heading><Text color="var(--am-native-muted, var(--am-panel-text-muted))" textAlign="center">{detail}</Text>{action}</VStack>;
}

export function AMProgress({ value, label }: { value: number; label: string }) {
	return <Box><HStack justify="space-between" mb={1}><Text fontSize="sm">{label}</Text><Text fontSize="sm">{Math.max(0, Math.min(100, value))}%</Text></HStack><Progress value={value} borderRadius="full" /></Box>;
}

export function AMOperationStatus({ operation, target, state, phase, progress }: { operation: string; target: string; state: string; phase?: string; progress?: number | null }) {
	return <HStack align="start" justify="space-between" wrap="wrap" p={3} borderBottom="1px solid" borderColor={panel.borderColor}><Box><Text fontWeight="semibold">{operation}</Text><Text dir="ltr" sx={{ unicodeBidi:"isolate" }} fontSize="sm" color="var(--am-native-muted, var(--am-panel-text-muted))">{target}{phase ? ` · ${phase}` : ""}</Text>{progress != null && <Box mt={2} minW="220px"><Progress value={progress} size="sm" borderRadius="full" /></Box>}</Box><AMStatusBadge state={state} /></HStack>;
}

export function AMLoading() { return <Flex minH="160px" align="center" justify="center" gap={3}><Spinner /><Text color="var(--am-panel-text-muted)">Loading operational data…</Text></Flex>; }

export function AMMetricGrid({ children }: { children: ReactNode }) { return <SimpleGrid columns={{ base: 1, sm: 2, xl: 4 }} spacing={4}>{children}</SimpleGrid>; }

export function AMActionButton({ children, onClick }: { children: ReactNode; onClick: () => void }) { return <Button variant="outline" onClick={onClick}>{children}</Button>; }

export function AMCommandBar({ value, onChange, placeholder, actions }: { value:string; onChange:(value:string)=>void; placeholder:string; actions?:ReactNode }) {
	return <Flex gap={3} wrap="wrap" align="center"><Input aria-label={placeholder} value={value} onChange={(event)=>onChange(event.target.value)} placeholder={placeholder} maxW="xl" {...panel} />{actions}</Flex>;
}

export function AMFilterBar({ children }: { children:ReactNode }) { return <HStack align="center" wrap="wrap" spacing={3} p={3} bg="var(--am-native-surface, var(--am-panel-surface))" border="1px solid" borderColor="var(--am-native-border, var(--am-panel-border))" borderRadius="lg">{children}</HStack>; }

export function AMVersionBadge({ version, channel, kind="running" }: { version?:string|null; channel?:string|null; kind?:"running"|"installed"|"desired" }) {
	return <HStack spacing={2}><Badge variant="subtle" colorScheme={kind==="running"?"green":kind==="desired"?"purple":"gray"}>{kind}</Badge><Text dir="ltr" sx={{ unicodeBidi:"isolate" }} fontFamily="mono" fontSize="sm">{version?.trim()||"Unknown"}</Text>{channel&&<Badge dir="ltr">{channel}</Badge>}</HStack>;
}

export function AMHealthBadge({ healthy, unavailable=false }: { healthy?:boolean; unavailable?:boolean }) {
	return <AMStatusBadge state={unavailable||healthy===undefined?"unknown":healthy?"healthy":"critical"} />;
}

export function AMDataGrid<Row>({ columns, rows, rowKey, empty }: { columns:Array<{key:string;label:string;render:(row:Row)=>ReactNode}>; rows:Row[]; rowKey:(row:Row)=>string|number; empty?:ReactNode }) {
	if (!rows.length) return <>{empty || <AMEmptyState title="No records" detail="No records match the current filters." />}</>;
	return <Box overflowX="auto" border="1px solid" borderColor="var(--am-native-border, var(--am-panel-border))" borderRadius="lg"><Table size="sm"><Thead><Tr>{columns.map((column)=><Th key={column.key}>{column.label}</Th>)}</Tr></Thead><Tbody>{rows.map((row)=><Tr key={rowKey(row)}>{columns.map((column)=><Td key={column.key}>{column.render(row)}</Td>)}</Tr>)}</Tbody></Table></Box>;
}

export function AMInspector({ isOpen, onClose, title, children, ...props }: {isOpen:boolean;onClose:()=>void;title:string;children:ReactNode} & Omit<ChakraDrawerProps,"isOpen"|"onClose"|"children">) {
	const { i18n } = useTranslation();
	const placement = i18n.dir(i18n.language) === "rtl" ? "left" : "right";
	return <ChakraDrawer isOpen={isOpen} onClose={onClose} placement={placement} size="lg" {...props}><DrawerOverlay /><DrawerContent bg="var(--am-native-surface, var(--am-panel-surface))" color="var(--am-native-text, var(--am-panel-text))"><DrawerCloseButton /><DrawerHeader borderBottom="1px solid" borderColor="var(--am-native-border, var(--am-panel-border))">{title}</DrawerHeader><DrawerBody py={5}>{children}</DrawerBody></DrawerContent></ChakraDrawer>;
}

export function AMTimeline({ items }: {items:Array<{id:string;title:string;detail?:string;time:string;state?:string}>}) {
	return <VStack align="stretch" spacing={0}>{items.map((item,index)=><HStack key={item.id} align="stretch" spacing={4}><VStack spacing={0} w={4}><Box w={3} h={3} mt={2} borderRadius="full" bg="var(--am-native-accent, var(--am-panel-accent))" /><Box flex="1" minH={index===items.length-1?0:8} w="1px" bg="var(--am-native-border, var(--am-panel-border))" /></VStack><Box pb={5}><HStack><Text fontWeight="semibold">{item.title}</Text>{item.state&&<AMStatusBadge state={item.state}/>}</HStack>{item.detail&&<Text fontSize="sm" color="var(--am-native-muted, var(--am-panel-text-muted))">{item.detail}</Text>}<Text fontSize="xs" color="var(--am-native-muted, var(--am-panel-text-muted))">{item.time}</Text></Box></HStack>)}</VStack>;
}

export function AMActionMenu({ actions }: {actions:Array<{label:string;onSelect:()=>void;isDisabled?:boolean}>}) {
	const { t } = useTranslation();
	return <Menu><MenuButton as={Button} variant="ghost" size="sm" aria-label={t("am.shell.moreActions")}><EllipsisVerticalIcon width={18}/></MenuButton><MenuList>{actions.map((action)=><MenuItem key={action.label} onClick={action.onSelect} isDisabled={action.isDisabled}>{action.label}</MenuItem>)}</MenuList></Menu>;
}

export function AMDrawer(props: {isOpen:boolean;onClose:()=>void;title:string;children:ReactNode}) { return <AMInspector {...props} />; }
