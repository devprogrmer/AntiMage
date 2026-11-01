import {
	Button,
	Box,
	Flex,
	Heading,
	Spinner,
	Text,
	useColorModeValue,
	VStack,
} from "@chakra-ui/react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

type DocsStatus = "checking" | "enabled" | "disabled";

export const ApiDocsPage = () => {
	const { t } = useTranslation();
	const [status, setStatus] = useState<DocsStatus>("checking");
	const panelBg = useColorModeValue("white", "gray.950");
	const borderColor = useColorModeValue("blackAlpha.200", "whiteAlpha.200");
	const mutedColor = useColorModeValue("gray.600", "gray.400");
	const frameBg = useColorModeValue("gray.50", "blackAlpha.300");

	useEffect(() => {
		let cancelled = false;
		void fetch("/openapi.json", {
			headers: { Accept: "application/json" },
			cache: "no-store",
		})
			.then((response) => {
				if (cancelled) return;
				if (response.ok) {
					setStatus("enabled");
					return;
				}
				setStatus("disabled");
			})
			.catch(() => {
				if (!cancelled) setStatus("disabled");
			});
		return () => {
			cancelled = true;
		};
	}, []);

	if (status === "checking") {
		return (
			<VStack spacing={4} align="stretch">
				<Flex minH="50vh" align="center" justify="center">
					<VStack spacing={3}>
						<Spinner />
						<Text color={mutedColor}>{t("apiDocs.checking")}</Text>
					</VStack>
				</Flex>
			</VStack>
		);
	}

	if (status === "disabled") {
		return (
			<VStack spacing={4} align="stretch">
				<VStack
					align="start"
					spacing={4}
					borderWidth="1px"
					borderColor={borderColor}
					borderRadius="md"
					bg={panelBg}
					p={{ base: 4, md: 6 }}
				>
					<Heading size="md">{t("apiDocs.disabledTitle")}</Heading>
					<Text color={mutedColor}>
						{t("apiDocs.disabledDescription")}
					</Text>
					<Button as="a" href="/docs/" variant="outline">
						{t("apiDocs.openRoute")}
					</Button>
				</VStack>
			</VStack>
		);
	}

	return (
		<VStack spacing={4} align="stretch">
			<Box
				borderWidth="1px"
				borderColor={borderColor}
				borderRadius="md"
				bg={panelBg}
				overflow="hidden"
				minH={{ base: "calc(100vh - 296px)", md: "calc(100vh - 270px)" }}
			>
				<Box
					as="iframe"
					title={t("apiDocs.menu")}
					src="/docs/"
					w="100%"
					h={{ base: "calc(100vh - 298px)", md: "calc(100vh - 272px)" }}
					minH={{ base: "460px", md: "500px" }}
					border="0"
					bg={frameBg}
				/>
			</Box>
		</VStack>
	);
};
